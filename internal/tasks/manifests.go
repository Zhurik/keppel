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

package tasks

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/keppel/internal/clair"
	"github.com/sapcc/keppel/internal/keppel"
)

//query that finds the next manifest to be validated
var outdatedManifestSearchQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT * FROM manifests
		WHERE validated_at < $1 OR (validated_at < $2 AND validation_error_message != '')
	ORDER BY validation_error_message != '' DESC, validated_at ASC
		-- oldest blobs first, but always prefer to recheck a failed validation
	LIMIT 1
		-- one at a time
`)

//ValidateNextManifest validates manifests that have not been validated for more
//than 6 hours. At most one manifest is validated per call. If no manifest
//needs to be validated, sql.ErrNoRows is returned.
func (j *Janitor) ValidateNextManifest() (returnErr error) {
	defer func() {
		if returnErr == nil {
			validateManifestSuccessCounter.Inc()
		} else if returnErr != sql.ErrNoRows {
			validateManifestFailedCounter.Inc()
			returnErr = fmt.Errorf("while validating a manifest: %s", returnErr.Error())
		}
	}()

	//find manifest: validate once every 24 hours, but recheck after 10 minutes if
	//validation failed
	var manifest keppel.Manifest
	maxSuccessfulValidatedAt := j.timeNow().Add(-24 * time.Hour)
	maxFailedValidatedAt := j.timeNow().Add(-10 * time.Minute)
	err := j.db.SelectOne(&manifest, outdatedManifestSearchQuery, maxSuccessfulValidatedAt, maxFailedValidatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			logg.Debug("no manifests to validate - slowing down...")
			return sql.ErrNoRows
		}
		return err
	}

	//find corresponding account and repo
	var repo keppel.Repository
	err = j.db.SelectOne(&repo, `SELECT * FROM repos WHERE id = $1`, manifest.RepositoryID)
	if err != nil {
		return fmt.Errorf("cannot find repo %d for manifest %s: %s", manifest.RepositoryID, manifest.Digest, err.Error())
	}
	account, err := keppel.FindAccount(j.db, repo.AccountName)
	if err != nil {
		return fmt.Errorf("cannot find account for manifest %s/%s: %s", repo.FullName(), manifest.Digest, err.Error())
	}

	//perform validation
	err = j.processor().ValidateExistingManifest(*account, repo, &manifest, j.timeNow())
	if err == nil {
		//update `validated_at` and reset error message
		_, err := j.db.Exec(`
			UPDATE manifests SET validated_at = $1, validation_error_message = ''
			 WHERE repo_id = $2 AND digest = $3`,
			j.timeNow(), repo.ID, manifest.Digest,
		)
		if err != nil {
			return err
		}
	} else {
		//attempt to log the error message, and also update the `validated_at`
		//timestamp to ensure that the ValidateNextManifest() loop does not get
		//stuck on this one
		_, updateErr := j.db.Exec(`
			UPDATE manifests SET validated_at = $1, validation_error_message = $2
			 WHERE repo_id = $3 AND digest = $4`,
			j.timeNow(), err.Error(), repo.ID, manifest.Digest,
		)
		if updateErr != nil {
			err = fmt.Errorf("%s (additional error encountered while recording validation error: %s)", err.Error(), updateErr.Error())
		}
		return err
	}

	return nil
}

var syncManifestRepoSelectQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT r.* FROM repos r
		JOIN accounts a ON r.account_name = a.name
		WHERE (r.next_manifest_sync_at IS NULL OR r.next_manifest_sync_at < $1)
		-- only consider repos in replica accounts
		AND (a.upstream_peer_hostname != '' OR a.external_peer_url != '')
	-- repos without any syncs first, then sorted by last sync
	ORDER BY r.next_manifest_sync_at IS NULL DESC, r.next_manifest_sync_at ASC
	-- only one repo at a time
	LIMIT 1
`)

var syncManifestEnumerateRefsQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT parent_digest, child_digest FROM manifest_manifest_refs WHERE repo_id = $1
`)

var syncManifestDoneQuery = keppel.SimplifyWhitespaceInSQL(`
	UPDATE repos SET next_manifest_sync_at = $2 WHERE id = $1
`)

//SyncManifestsInNextRepo finds the next repository in a replica account where
//manifests have not been synced for more than an hour, and syncs its manifests.
//Syncing involves checking with the primary account which manifests have been
//deleted there, and replicating the deletions on our side.
//
//If no repo needs syncing, sql.ErrNoRows is returned.
func (j *Janitor) SyncManifestsInNextRepo() (returnErr error) {
	defer func() {
		if returnErr == nil {
			syncManifestsSuccessCounter.Inc()
		} else if returnErr != sql.ErrNoRows {
			syncManifestsFailedCounter.Inc()
			returnErr = fmt.Errorf("while syncing manifests in a replica repo: %s", returnErr.Error())
		}
	}()

	//find repository to sync
	var repo keppel.Repository
	err := j.db.SelectOne(&repo, syncManifestRepoSelectQuery, j.timeNow())
	if err != nil {
		if err == sql.ErrNoRows {
			logg.Debug("no accounts to sync manifests in - slowing down...")
			return sql.ErrNoRows
		}
		return err
	}

	//find corresponding account
	account, err := keppel.FindAccount(j.db, repo.AccountName)
	if err != nil {
		return fmt.Errorf("cannot find account for repo %s: %s", repo.FullName(), err.Error())
	}

	//do not perform manifest sync while account is in maintenance (maintenance mode blocks all kinds of replication)
	if !account.InMaintenance {
		err = j.performManifestSync(*account, repo)
		if err != nil {
			return err
		}
	}

	_, err = j.db.Exec(syncManifestDoneQuery, repo.ID, j.timeNow().Add(1*time.Hour))
	return err
}

func (j *Janitor) performManifestSync(account keppel.Account, repo keppel.Repository) error {
	//enumerate manifests in this repo
	var manifests []keppel.Manifest
	_, err := j.db.Select(&manifests, `SELECT * FROM manifests WHERE repo_id = $1`, repo.ID)
	if err != nil {
		return fmt.Errorf("cannot list manifests in repo %s: %s", repo.FullName(), err.Error())
	}

	//check which manifests need to be deleted
	shallDeleteManifest := make(map[string]bool)
	p := j.processor()
	for _, manifest := range manifests {
		ref := keppel.ManifestReference{Digest: digest.Digest(manifest.Digest)}
		exists, err := p.CheckManifestOnPrimary(account, repo, ref)
		if err != nil {
			return fmt.Errorf("cannot check existence of manifest %s/%s on primary account: %s", repo.FullName(), manifest.Digest, err.Error())
		}
		if !exists {
			shallDeleteManifest[manifest.Digest] = true
		}
	}

	//enumerate manifest-manifest refs in this repo
	parentDigestsOf := make(map[string][]string)
	err = keppel.ForeachRow(j.db, syncManifestEnumerateRefsQuery, []interface{}{repo.ID}, func(rows *sql.Rows) error {
		var (
			parentDigest string
			childDigest  string
		)
		err = rows.Scan(&parentDigest, &childDigest)
		if err != nil {
			return err
		}
		parentDigestsOf[childDigest] = append(parentDigestsOf[childDigest], parentDigest)
		return nil
	})
	if err != nil {
		return fmt.Errorf("cannot enumerate manifest-manifest refs in repo %s: %s", repo.FullName(), err.Error())
	}

	//delete manifests in correct order (if there is a parent-child relationship,
	//we always need to delete the parent manifest first, otherwise the database
	//will complain because of its consistency checks)
	if len(shallDeleteManifest) > 0 {
		logg.Info("deleting %d manifests in repo %s that were deleted on corresponding primary account", len(shallDeleteManifest), repo.FullName())
	}
	manifestWasDeleted := make(map[string]bool)
	for len(shallDeleteManifest) > 0 {
		deletedSomething := false
	MANIFEST:
		for digest := range shallDeleteManifest {
			for _, parentDigest := range parentDigestsOf[digest] {
				if !manifestWasDeleted[parentDigest] {
					//cannot delete this manifest yet because it's still being referenced - retry in next iteration
					continue MANIFEST
				}
			}

			//no manifests left that reference this one - we can delete it
			//
			//The ordering is important: The DELETE statement could fail if some concurrent
			//process created a manifest reference in the meantime. If that happens,
			//and we have already deleted the manifest in the backing storage, we've
			//caused an inconsistency that we cannot recover from. To avoid that
			//risk, we do it the other way around. In this way, we could have an
			//inconsistency where the manifest is deleted from the database, but still
			//present in the backing storage. But this inconsistency is easier to
			//recover from: SweepStorageInNextAccount will take care of it soon
			//enough. Also the user will not notice this inconsistency because the DB
			//is our primary source of truth.
			_, err := j.db.Delete(&keppel.Manifest{RepositoryID: repo.ID, Digest: digest}) //without transaction: we need this committed right now

			if err != nil {
				return fmt.Errorf("cannot remove deleted manifest %s in repo %s from DB: %s", digest, repo.FullName(), err.Error())
			}
			err = j.sd.DeleteManifest(account, repo.Name, digest)
			if err != nil {
				return fmt.Errorf("cannot remove deleted manifest %s in repo %s from storage: %s", digest, repo.FullName(), err.Error())
			}

			//remove deletion from work queue (so that we can eventually exit from the outermost loop)
			delete(shallDeleteManifest, digest)

			//track deletion (so that we can eventually start deleting manifests referenced by this one)
			manifestWasDeleted[digest] = true

			//track that we're making progress
			deletedSomething = true
		}

		//we should be deleting something in each iteration, otherwise we will get stuck in an infinite loop
		if !deletedSomething {
			undeletedDigests := make([]string, 0, len(shallDeleteManifest))
			for digest := range shallDeleteManifest {
				undeletedDigests = append(undeletedDigests, digest)
			}
			return fmt.Errorf("cannot remove deleted manifests %v in repo %s because they are still being referenced by other manifests (this smells like an inconsistency on the primary account)",
				undeletedDigests, repo.FullName())
		}
	}

	return nil
}

var vulnCheckSelectQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT m.* FROM manifests m
		WHERE (m.next_vuln_check_at IS NULL OR m.next_vuln_check_at < $1)
	-- manifests without any check first, then sorted by schedule
	ORDER BY m.next_vuln_check_at IS NULL DESC, m.next_vuln_check_at ASC
	-- only one manifests at a time
	LIMIT 1
`)

var vulnCheckBlobSelectQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT b.* FROM blobs b
	JOIN manifest_blob_refs r ON b.id = r.blob_id
		WHERE r.repo_id = $1 AND r.digest = $2
`)

var vulnCheckSubmanifestInfoQuery = keppel.SimplifyWhitespaceInSQL(`
	SELECT m.vuln_status FROM manifests m
	JOIN manifest_manifest_refs r ON m.digest = r.child_digest
		WHERE r.parent_digest = $1
`)

//CheckVulnerabilitiesForNextManifest finds the next manifest that has not been
//checked for vulnerabilities yet (or within the last hour), and runs the
//vulnerability check by submitting the image to Clair.
//
//This assumes that `j.cfg.Clair != nil`.
//
//If no manifest needs checking, sql.ErrNoRows is returned.
func (j *Janitor) CheckVulnerabilitiesForNextManifest() (returnErr error) {
	defer func() {
		if returnErr == nil {
			checkVulnerabilitySuccessCounter.Inc()
		} else if returnErr != sql.ErrNoRows {
			checkVulnerabilityFailedCounter.Inc()
			returnErr = fmt.Errorf("while updating vulnerability status for a manifest: %s", returnErr.Error())
		}
	}()

	//find manifest to sync
	var manifest keppel.Manifest
	err := j.db.SelectOne(&manifest, vulnCheckSelectQuery, j.timeNow())
	if err != nil {
		if err == sql.ErrNoRows {
			logg.Debug("no manifests to update vulnerability status for - slowing down...")
			return sql.ErrNoRows
		}
		return err
	}

	//load corresponding repo and account
	repo, err := keppel.FindRepositoryByID(j.db, manifest.RepositoryID)
	if err != nil {
		return fmt.Errorf("cannot find repo for manifest %s: %s", manifest.Digest, err.Error())
	}
	account, err := keppel.FindAccount(j.db, repo.AccountName)
	if err != nil {
		return fmt.Errorf("cannot find account for repo %s: %s", repo.FullName(), err.Error())
	}

	err = j.doVulnerabilityCheck(*account, *repo, &manifest)
	if err != nil {
		return err
	}
	_, err = j.db.Update(&manifest)
	return err
}

func (j *Janitor) doVulnerabilityCheck(account keppel.Account, repo keppel.Repository, manifest *keppel.Manifest) error {
	//skip validation while account is in maintenance (maintenance mode blocks
	//all kinds of activity on an account's contents)
	if account.InMaintenance {
		manifest.NextVulnerabilityCheckAt = p2time(j.timeNow().Add(1 * time.Hour))
		return nil
	}

	//we need all blobs directly referenced by this manifest (we do not care
	//about submanifests at this level, the reports from those will be merged
	//later on in the API)
	var blobs []keppel.Blob
	_, err := j.db.Select(&blobs, vulnCheckBlobSelectQuery, manifest.RepositoryID, manifest.Digest)
	if err != nil {
		return err
	}

	//collect blob data to construct Clair manifest
	clairManifest := clair.Manifest{
		Digest: manifest.Digest,
	}
	for _, blob := range blobs {
		//can only validate when all blobs are present in the storage
		if blob.StorageID == "" {
			//if the manifest is fairly new, the user who replicated it is probably
			//still replicating it; give them 10 minutes to finish replicating it
			manifest.NextVulnerabilityCheckAt = p2time(manifest.PushedAt.Add(10 * time.Minute))
			if manifest.NextVulnerabilityCheckAt.Before(time.Now()) {
				return nil
			}
			//otherwise we do the replication ourselves
			_, err := j.processor().ReplicateBlob(blob, account, repo, nil)
			if err != nil {
				return err
			}
			//after successful replication, restart this call to read the new blob with the correct StorageID from the DB
			return j.doVulnerabilityCheck(account, repo, manifest)
		}

		blobURL, err := j.sd.URLForBlob(account, blob.StorageID)
		//TODO handle ErrCannotGenerateURL (at least enough to cover unit tests)
		if err != nil {
			return err
		}
		clairManifest.Layers = append(clairManifest.Layers, clair.Layer{
			Digest: blob.Digest,
			URL:    blobURL,
		})
	}

	//collect vulnerability status of constituent images
	var severities []clair.Severity
	err = keppel.ForeachRow(j.db, vulnCheckSubmanifestInfoQuery, []interface{}{manifest.Digest}, func(rows *sql.Rows) error {
		var severity clair.Severity
		err := rows.Scan(&severity)
		severities = append(severities, severity)
		return err
	})
	if err != nil {
		return err
	}

	//ask Clair for vulnerability status of blobs in this image
	if len(blobs) > 0 {
		clairState, err := j.cfg.ClairClient.CheckManifestState(clairManifest)
		if err != nil {
			return err
		}
		if clairState.IsErrored {
			return fmt.Errorf("Clair reports indexing of %s as errored", manifest.Digest)
		}
		if clairState.IsIndexed {
			clairReport, err := j.cfg.ClairClient.GetVulnerabilityReport(manifest.Digest)
			if err != nil {
				return err
			}
			if clairReport == nil {
				return fmt.Errorf("Clair reports indexing of %s as finished, but vulnerability report is 404", manifest.Digest)
			}
			severities = append(severities, clairReport.Severity())
		} else {
			severities = append(severities, clair.UnknownSeverity)
		}
	}

	//merge all vulnerability statuses
	manifest.VulnerabilityStatus = clair.MergeSeverities(severities...)
	if manifest.VulnerabilityStatus == clair.UnknownSeverity {
		logg.Info("skipping vulnerability check for %s: indexing is not finished yet", manifest.Digest)
		//wait a bit for indexing to finish, then come back to update the vulnerability status
		manifest.NextVulnerabilityCheckAt = p2time(j.timeNow().Add(2 * time.Minute))
	} else {
		//regular recheck loop (vulnerability status might change if Clair adds new vulnerabilities to its DB)
		manifest.NextVulnerabilityCheckAt = p2time(j.timeNow().Add(1 * time.Hour))
	}
	return nil
}

func p2time(x time.Time) *time.Time {
	return &x
}
