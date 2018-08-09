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

package database

import "strings"

var sqlMigrations = stripWhitespace(map[string]string{
	"001_initial.up.sql": `
		BEGIN;
		CREATE TABLE accounts (
			name           TEXT NOT NULL PRIMARY KEY,
			auth_tenant_id TEXT NOT NULL
		);
		COMMIT;
	`,
	"001_initial.down.sql": `
		BEGIN;
		DROP TABLE accounts;
		COMMIT;
	`,
})

func stripWhitespace(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for filename, sql := range in {
		out[filename] = strings.Replace(
			strings.Join(strings.Fields(sql), " "),
			"; ", ";\n", -1,
		)
	}
	return out
}
