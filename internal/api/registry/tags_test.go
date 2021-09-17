/******************************************************************************
*
*  Copyright 2020 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package registryv2_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/keppel/internal/keppel"
	"github.com/sapcc/keppel/internal/test"
)

func TestListTags(t *testing.T) {
	testWithPrimary(t, nil, func(h http.Handler, cfg keppel.Configuration, db *keppel.DB, ad *test.AuthDriver, sd *test.StorageDriver, fd *test.FederationDriver, clock *test.Clock, auditor *test.Auditor) {

		token := getToken(t, h, ad, "repository:test1/foo:pull,push",
			keppel.CanPullFromAccount,
			keppel.CanPushToAccount)
		readOnlyToken := getToken(t, h, ad, "repository:test1/foo:pull",
			keppel.CanPullFromAccount)

		//test tag list for missing repo
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v2/test1/foo/tags/list",
			Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
			ExpectStatus: http.StatusNotFound,
			ExpectHeader: test.VersionHeader,
			ExpectBody:   test.ErrorCode(keppel.ErrNameUnknown),
		}.Check(t, h)

		//upload a test image without tagging it
		image := test.GenerateImage( /* no layers */ )
		image.MustUpload(t, h, db, token, fooRepoRef, "")

		//test empty tag list for existing repo
		req := assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v2/test1/foo/tags/list",
			Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
			ExpectStatus: http.StatusOK,
			ExpectHeader: test.VersionHeader,
			ExpectBody:   assert.JSONObject{"name": "test1/foo", "tags": []string{}},
		}
		req.Check(t, h)

		//query parameters do not influence this result
		req.Path = "/v2/test1/foo/tags/list?n=10"
		req.Check(t, h)
		req.Path = "/v2/test1/foo/tags/list?n=10&last=foo"
		req.Check(t, h)

		//generate pseudo-random, but deterministic tag names
		allTagNames := make([]string, 10)
		for idx := range allTagNames {
			allTagNames[idx] = sha256Of([]byte(strconv.Itoa(idx)))
		}

		//upload test image under all of them (in randomized order!)
		rand.Shuffle(len(allTagNames), func(i, j int) {
			allTagNames[i], allTagNames[j] = allTagNames[j], allTagNames[i]
		})
		for _, tagName := range allTagNames {
			image.MustUpload(t, h, db, token, fooRepoRef, tagName)
		}
		//but when listing tags, we expect them in sorted order
		sort.Strings(allTagNames)

		//test unpaginated
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v2/test1/foo/tags/list",
			Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
			ExpectStatus: http.StatusOK,
			ExpectHeader: test.VersionHeader,
			ExpectBody:   assert.JSONObject{"name": "test1/foo", "tags": allTagNames},
		}.Check(t, h)

		//test paginated
		for offset := 0; offset < len(allTagNames); offset++ {
			for length := 1; length <= len(allTagNames)+1; length++ {
				expectedPage := allTagNames[offset:]
				expectedHeaders := map[string]string{
					test.VersionHeaderKey: test.VersionHeaderValue,
					"Content-Type":        "application/json",
				}

				if len(expectedPage) > length {
					expectedPage = expectedPage[:length]
					lastRepoName := expectedPage[len(expectedPage)-1]
					expectedHeaders["Link"] = fmt.Sprintf(`</v2/test1/foo/tags/list?last=%s&n=%d>; rel="next"`,
						strings.Replace(lastRepoName, "/", "%2F", -1), length,
					)
				}

				path := fmt.Sprintf(`/v2/test1/foo/tags/list?n=%d`, length)
				if offset > 0 {
					path += `&last=` + allTagNames[offset-1]
				}

				assert.HTTPRequest{
					Method:       "GET",
					Path:         path,
					Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
					ExpectStatus: http.StatusOK,
					ExpectHeader: expectedHeaders,
					ExpectBody:   assert.JSONObject{"name": "test1/foo", "tags": expectedPage},
				}.Check(t, h)
			}
		}

		//test error cases for pagination query params
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v2/test1/foo/tags/list?n=-1",
			Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
			ExpectStatus: http.StatusBadRequest,
			ExpectHeader: test.VersionHeader,
			ExpectBody:   assert.StringData("invalid value for \"n\": strconv.ParseUint: parsing \"-1\": invalid syntax\n"),
		}.Check(t, h)
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v2/test1/foo/tags/list?n=0",
			Header:       map[string]string{"Authorization": "Bearer " + readOnlyToken},
			ExpectStatus: http.StatusBadRequest,
			ExpectHeader: test.VersionHeader,
			ExpectBody:   assert.StringData("invalid value for \"n\": must not be 0\n"),
		}.Check(t, h)

		//test anycast tag listing
		if currentlyWithAnycast {
			testWithReplica(t, h, db, clock, "on_first_use", func(firstPass bool, h2 http.Handler, cfg2 keppel.Configuration, db2 *keppel.DB, ad2 *test.AuthDriver, sd2 *test.StorageDriver) {
				testAnycast(t, firstPass, db2, func() {
					anycastToken := getTokenForAnycast(t, h, ad, "repository:test1/foo:pull",
						keppel.CanPullFromAccount)
					req := assert.HTTPRequest{
						Method: "GET",
						Path:   "/v2/test1/foo/tags/list",
						Header: map[string]string{
							"Authorization":     "Bearer " + anycastToken,
							"X-Forwarded-Host":  cfg.AnycastAPIPublicURL.Host,
							"X-Forwarded-Proto": cfg.AnycastAPIPublicURL.Scheme,
						},
						ExpectStatus: http.StatusOK,
						ExpectHeader: test.VersionHeader,
						ExpectBody:   assert.JSONObject{"name": "test1/foo", "tags": allTagNames},
					}
					req.Check(t, h)
					req.Check(t, h2)
				})
			})
		}
	})
}
