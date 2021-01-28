/*******************************************************************************
*
* Copyright 2021 SAP SE
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

package clair

import "testing"

func TestMergeSeverities(t *testing.T) {
	expect := func(expected, actual Severity) {
		t.Helper()
		if expected != actual {
			t.Errorf("expected %s, but got %s", expected, actual)
		}
	}
	expect(CleanSeverity, MergeSeverities())
	expect(PendingSeverity, MergeSeverities(PendingSeverity))
	expect(PendingSeverity, MergeSeverities(PendingSeverity, HighSeverity))
	expect(LowSeverity, MergeSeverities(LowSeverity, LowSeverity))
	expect(HighSeverity, MergeSeverities(LowSeverity, HighSeverity))
}
