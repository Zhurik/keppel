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

package test

import (
	"context"
	"errors"
	"net/http"

	"github.com/sapcc/keppel/pkg/keppel"
)

//Implements all three Driver interfaces. All methods return errors or empty
//values all the time, except for initialization methods (ReadConfig, Connect)
//which return nil.
type noopDriver struct{}

func init() {
	keppel.RegisterAuthDriver("noop", func() keppel.AuthDriver { return &noopDriver{} })
	keppel.RegisterOrchestrationDriver("noop", func() keppel.OrchestrationDriver { return &noopDriver{} })
	keppel.RegisterStorageDriver("noop", func() keppel.StorageDriver { return &noopDriver{} })
}

func (*noopDriver) ReadConfig(unmarshal func(interface{}) error) error {
	return nil
}

func (*noopDriver) Connect() error {
	return nil
}

func (*noopDriver) ValidateTenantID(tenantID string) error {
	return nil
}

func (*noopDriver) SetupAccount(account keppel.Account, an keppel.Authorization) error {
	return errors.New("SetupAccount not implemented for NoopDriver")
}

func (*noopDriver) AuthenticateUser(userName, password string) (keppel.Authorization, *keppel.RegistryV2Error) {
	return nil, keppel.ErrUnsupported.With("AuthenticateUser not implemented for NoopDriver")
}

func (*noopDriver) AuthenticateUserFromRequest(r *http.Request) (keppel.Authorization, *keppel.RegistryV2Error) {
	return nil, keppel.ErrUnsupported.With("AuthenticateUserFromRequest not implemented for NoopDriver")
}

func (*noopDriver) GetEnvironment(account keppel.Account, driver keppel.AuthDriver) ([]string, error) {
	return nil, errors.New("GetEnvironment not implemented for NoopDriver")
}

func (*noopDriver) DoHTTPRequest(account keppel.Account, r *http.Request) (*http.Response, error) {
	return nil, errors.New("DoHTTPRequest not implemented for NoopDriver")
}

func (*noopDriver) Run(ctx context.Context) (ok bool) {
	return false
}
