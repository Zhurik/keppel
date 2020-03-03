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

package processor

import (
	"database/sql"
	"encoding/json"
	"io/ioutil"
	"strings"
	"time"

	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/ocischema"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/sapcc/keppel/internal/keppel"
	"gopkg.in/gorp.v2"
)

//IncomingManifest contains information about a manifest uploaded by the user
//(or downloaded from a peer registry in the case of replication).
type IncomingManifest struct {
	RepoName  string
	Reference keppel.ManifestReference
	MediaType string
	Contents  []byte
	PushedAt  time.Time //usually time.Now(), but can be different in unit tests
}

//ValidateAndStoreManifest validates the given manifest and stores it under the
//given reference. If the reference is a digest, it is validated. Otherwise, a
//tag with that name is created that points to the new manifest.
func (p *Processor) ValidateAndStoreManifest(account keppel.Account, m IncomingManifest) (*keppel.Manifest, error) {
	//early preparations
	err := p.checkQuotaForManifestPush(account)
	if err != nil {
		return nil, err
	}
	repo, err := p.db.FindOrCreateRepository(m.RepoName, account)
	if err != nil {
		return nil, err
	}

	//validate manifest
	manifest, manifestDesc, err := distribution.UnmarshalManifest(m.MediaType, m.Contents)
	if err != nil {
		return nil, keppel.ErrManifestInvalid.With(err.Error())
	}
	if m.Reference.IsDigest() && manifestDesc.Digest != m.Reference.Digest {
		return nil, keppel.ErrDigestInvalid.With("actual manifest digest is " + manifestDesc.Digest.String())
	}

	var dbManifest *keppel.Manifest
	err = p.insideTransaction(func(tx *gorp.Transaction) error {
		//when a manifest is pushed into an account with replication enabled, it's
		//because we're replicating a manifest from upstream; in this case, the
		//referenced blobs and manifests will be replicated later and we skip the
		//corresponding validation steps
		if account.UpstreamPeerHostName == "" {
			//check that all referenced blobs exist (TODO: some manifest types reference
			//other manifests, so we should look for manifests in these cases)
			for _, desc := range manifest.References() {
				_, err := keppel.FindBlobByRepositoryID(tx, desc.Digest, repo.ID, account)
				if err == sql.ErrNoRows {
					return keppel.ErrManifestBlobUnknown.With("").WithDetail(desc.Digest.String())
				}
				if err != nil {
					return err
				}
			}

			//enforce account-specific validation rules on manifest
			if account.RequiredLabels != "" {
				requiredLabels := strings.Split(account.RequiredLabels, ",")
				missingLabels, err := checkManifestHasRequiredLabels(tx, p.sd, account, manifest, requiredLabels)
				if err != nil {
					return err
				}
				if len(missingLabels) > 0 {
					msg := "missing required labels: " + strings.Join(missingLabels, ", ")
					return keppel.ErrManifestInvalid.With(msg)
				}
			}
		}

		//compute total size of image
		sizeBytes := uint64(manifestDesc.Size)
		for _, desc := range manifest.References() {
			sizeBytes += uint64(desc.Size)
		}

		//create new database entries
		dbManifest = &keppel.Manifest{
			RepositoryID: repo.ID,
			Digest:       manifestDesc.Digest.String(),
			MediaType:    manifestDesc.MediaType,
			SizeBytes:    sizeBytes,
			PushedAt:     m.PushedAt,
			ValidatedAt:  m.PushedAt,
		}
		err = dbManifest.InsertIfMissing(tx)
		if err != nil {
			return err
		}
		if m.Reference.IsTag() {
			err = keppel.Tag{
				RepositoryID: repo.ID,
				Name:         m.Reference.Tag,
				Digest:       manifestDesc.Digest.String(),
				PushedAt:     m.PushedAt,
			}.InsertIfMissing(tx)
			if err != nil {
				return err
			}
		}

		//PUT the manifest in the backend
		return p.sd.WriteManifest(account, repo.Name, manifestDesc.Digest.String(), m.Contents)
	})
	return dbManifest, err
}

//Returns the list of missing labels, or nil if everything is ok.
func checkManifestHasRequiredLabels(tx *gorp.Transaction, sd keppel.StorageDriver, account keppel.Account, manifest distribution.Manifest, requiredLabels []string) ([]string, error) {
	var configBlob distribution.Descriptor
	switch m := manifest.(type) {
	case *schema2.DeserializedManifest:
		configBlob = m.Config
	case *ocischema.DeserializedManifest:
		configBlob = m.Config
	case *manifestlist.DeserializedManifestList:
		//manifest lists only reference other manifests, they don't have labels themselves
		return nil, nil
	}

	//load the config blob
	storageID, err := tx.SelectStr(
		`SELECT storage_id FROM blobs WHERE account_name = $1 AND digest = $2`,
		account.Name, configBlob.Digest.String(),
	)
	if err != nil {
		return nil, err
	}
	if storageID == "" {
		return nil, keppel.ErrManifestBlobUnknown.With("").WithDetail(configBlob.Digest.String())
	}
	blobReader, _, err := sd.ReadBlob(account, storageID)
	if err != nil {
		return nil, err
	}
	blobContents, err := ioutil.ReadAll(blobReader)
	if err != nil {
		return nil, err
	}
	err = blobReader.Close()
	if err != nil {
		return nil, err
	}

	//the Docker v2 and OCI formats are very similar; they're both JSON and have
	//the labels in the same place, so we can use a single code path for both
	var data struct {
		Config struct {
			Labels map[string]interface{} `json:"labels"`
		} `json:"config"`
	}
	err = json.Unmarshal(blobContents, &data)
	if err != nil {
		return nil, err
	}

	var missingLabels []string
	for _, label := range requiredLabels {
		if _, exists := data.Config.Labels[label]; !exists {
			missingLabels = append(missingLabels, label)
		}
	}
	return missingLabels, nil
}
