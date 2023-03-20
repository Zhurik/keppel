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

package client

import (
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"

	"github.com/sapcc/keppel/internal/keppel"
)

// ValidationLogger can be passed to ValidateManifest, primarily to allow the
// caller to log the progress of the validation operation.
type ValidationLogger interface {
	LogManifest(reference keppel.ManifestReference, level int, validationResult error, resultFromCache bool)
	LogBlob(d digest.Digest, level int, validationResult error, resultFromCache bool)
}

type noopLogger struct{}

func (noopLogger) LogManifest(keppel.ManifestReference, int, error, bool) {}
func (noopLogger) LogBlob(digest.Digest, int, error, bool)                {}

// ValidationSession holds state and caches intermediate results over the
// course of several ValidateManifest() and ValidateBlobContents() calls.
// The cache optimizes the validation of submanifests and blobs that are
// referenced multiple times. The session instance should only be used for as
// long as the caller wishes to cache validation results.
type ValidationSession struct {
	Logger  ValidationLogger
	isValid map[string]bool
}

func (s *ValidationSession) applyDefaults() *ValidationSession {
	if s == nil {
		//This branch is taken when the caller supplied `nil` for the
		//*ValidationSession argument in ValidateManifest or ValidateBlobContents.
		s = &ValidationSession{}
	}
	if s.Logger == nil {
		s.Logger = noopLogger{}
	}
	if s.isValid == nil {
		s.isValid = make(map[string]bool)
	}
	return s
}

func (c *RepoClient) validationCacheKey(digestOrTagName string) string {
	// We allow sharing a ValidationSession between multiple RepoClients to keep
	// the API simple. But we cannot share validation results between repos: For
	// any given digest, validation could succeed in one repo, fail in a second
	// repo, and fail *in a different way* in the third repo. Therefore we need
	// to store validation results keyed by digest *and* repo URL.
	return fmt.Sprintf("%s/%s/%s", c.Host, c.RepoName, digestOrTagName)
}

// ValidateManifest fetches the given manifest from the repo and verifies that
// it parses correctly. It also validates all references manifests and blobs
// recursively.
func (c *RepoClient) ValidateManifest(reference keppel.ManifestReference, session *ValidationSession, platformFilter keppel.PlatformFilter) error {
	return c.doValidateManifest(reference, 0, session.applyDefaults(), platformFilter)
}

func (c *RepoClient) doValidateManifest(reference keppel.ManifestReference, level int, session *ValidationSession, platformFilter keppel.PlatformFilter) (returnErr error) {
	if session.isValid[c.validationCacheKey(reference.String())] {
		session.Logger.LogManifest(reference, level, nil, true)
		return nil
	}

	logged := false
	defer func() {
		if !logged {
			session.Logger.LogManifest(reference, level, returnErr, false)
		}
	}()

	manifestBytes, manifestMediaType, err := c.DownloadManifest(reference, nil)
	if err != nil {
		return err
	}
	manifest, manifestDesc, err := keppel.ParseManifest(manifestMediaType, manifestBytes)
	if err != nil {
		return err
	}

	//the manifest itself looks good...
	session.Logger.LogManifest(keppel.ManifestReference{Digest: manifestDesc.Digest}, level, nil, false)
	logged = true

	//...now recurse into the manifests and blobs that it references
	for _, desc := range manifest.BlobReferences() {
		err := c.doValidateBlobContents(desc.Digest, level+1, session)
		if err != nil {
			return err
		}
	}
	for _, desc := range manifest.ManifestReferences(platformFilter) {
		err := c.doValidateManifest(keppel.ManifestReference{Digest: desc.Digest}, level+1, session, platformFilter)
		if err != nil {
			return err
		}
	}

	//write validity into cache only after all references have been validated as well
	session.isValid[c.validationCacheKey(manifestDesc.Digest.String())] = true
	session.isValid[c.validationCacheKey(reference.String())] = true
	return nil
}

// ValidateBlobContents fetches the given blob from the repo and verifies that
// the contents produce the correct digest.
func (c *RepoClient) ValidateBlobContents(blobDigest digest.Digest, session *ValidationSession) error {
	return c.doValidateBlobContents(blobDigest, 0, session.applyDefaults())
}

func (c *RepoClient) doValidateBlobContents(blobDigest digest.Digest, level int, session *ValidationSession) (returnErr error) {
	cacheKey := c.validationCacheKey(blobDigest.String())
	if session.isValid[cacheKey] {
		session.Logger.LogBlob(blobDigest, level, nil, true)
		return nil
	}
	defer func() {
		session.Logger.LogBlob(blobDigest, level, returnErr, false)
	}()

	readCloser, _, err := c.DownloadBlob(blobDigest)
	if err != nil {
		return err
	}

	defer func() {
		if returnErr == nil {
			returnErr = readCloser.Close()
		} else {
			readCloser.Close()
		}
	}()

	hash := blobDigest.Algorithm().Hash()
	_, err = io.Copy(hash, readCloser)
	if err != nil {
		return err
	}
	actualDigest := digest.NewDigest(blobDigest.Algorithm(), hash)
	if actualDigest != blobDigest {
		return fmt.Errorf("actual digest is %s", actualDigest)
	}

	session.isValid[cacheKey] = true
	return nil
}
