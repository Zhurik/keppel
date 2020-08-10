/*******************************************************************************
*
* Copyright 2018 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package keppel

import (
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"regexp"

	"github.com/docker/libtrust"
	"github.com/sapcc/go-bits/logg"
)

//APIAccessURL describes where Keppel's API is reachable. Typically only the
//protocol and host/port fields are filled.
type APIAccessURL struct {
	url.URL
}

//Hostname returns the hostname from this URL.
func (u APIAccessURL) Hostname() string {
	hostAndMaybePort := u.Host
	host, _, err := net.SplitHostPort(hostAndMaybePort)
	if err == nil {
		return host
	}
	return hostAndMaybePort //looks like there is no port in here after all
}

//SameHostAndSchemeAs is self-explanatory.
func (u APIAccessURL) SameHostAndSchemeAs(other APIAccessURL) bool {
	return u.Scheme == other.Scheme && u.Host == other.Host
}

//Configuration contains all configuration values that are not specific to a
//certain driver.
type Configuration struct {
	APIPublicURL        APIAccessURL
	AnycastAPIPublicURL *APIAccessURL
	DatabaseURL         url.URL
	JWTIssuerKey        libtrust.PrivateKey
	AnycastJWTIssuerKey *libtrust.PrivateKey
}

var (
	looksLikePEMRx    = regexp.MustCompile(`^\s*-----\s*BEGIN`)
	stripWhitespaceRx = regexp.MustCompile(`(?m)^\s*|\s*$`)
)

//ParseIssuerKey parses the contents of the KEPPEL_ISSUER_KEY variable.
func ParseIssuerKey(in string) (libtrust.PrivateKey, error) {
	//if it looks like PEM, it's probably PEM; otherwise it's a filename
	var buf []byte
	if looksLikePEMRx.MatchString(in) {
		buf = []byte(in)
	} else {
		var err error
		buf, err = ioutil.ReadFile(in)
		if err != nil {
			return nil, err
		}
	}
	buf = stripWhitespaceRx.ReplaceAll(buf, nil)

	return libtrust.UnmarshalPrivateKeyPEM(buf)
}

//ParseConfiguration obtains a keppel.Configuration instance from the
//corresponding environment variables. Aborts on error.
func ParseConfiguration() Configuration {
	cfg := Configuration{
		APIPublicURL:        APIAccessURL{URL: mustGetenvURL("KEPPEL_API_PUBLIC_URL")},
		AnycastAPIPublicURL: mayGetenvURL("KEPPEL_API_ANYCAST_URL"),
		DatabaseURL:         mustGetenvURL("KEPPEL_DB_URI"),
	}

	var err error
	cfg.JWTIssuerKey, err = ParseIssuerKey(MustGetenv("KEPPEL_ISSUER_KEY"))
	if err != nil {
		logg.Fatal("failed to read KEPPEL_ISSUER_KEY: " + err.Error())
	}

	if cfg.AnycastAPIPublicURL != nil {
		key, err := ParseIssuerKey(MustGetenv("KEPPEL_ANYCAST_ISSUER_KEY"))
		if err != nil {
			logg.Fatal("failed to read KEPPEL_ANYCAST_ISSUER_KEY: " + err.Error())
		}
		cfg.AnycastJWTIssuerKey = &key
	}

	return cfg
}

//MustGetenv is like os.Getenv, but aborts with an error message if the given
//environment variable is missing or empty.
func MustGetenv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		logg.Fatal("missing environment variable: %s", key)
	}
	return val
}

func mustGetenvURL(key string) url.URL {
	val := MustGetenv(key)
	parsed, err := url.Parse(val)
	if err != nil {
		logg.Fatal("malformed %s: %s", key, err.Error())
	}
	return *parsed
}

func mayGetenvURL(key string) *APIAccessURL {
	val := os.Getenv(key)
	if val == "" {
		return nil
	}
	parsed, err := url.Parse(val)
	if err != nil {
		logg.Fatal("malformed %s: %s", key, err.Error())
	}
	return &APIAccessURL{URL: *parsed}
}
