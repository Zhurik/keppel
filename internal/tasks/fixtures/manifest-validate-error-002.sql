INSERT INTO accounts (name, auth_tenant_id, upstream_peer_hostname, required_labels) VALUES ('test1', 'test1authtenant', '', '');

INSERT INTO blob_mounts (blob_id, repo_id) VALUES (1, 1);

INSERT INTO blobs (id, account_name, digest, size_bytes, storage_id, pushed_at, validated_at, validation_error_message) VALUES (1, 'test1', 'sha256:712dfd307e9f735a037e1391f16c8747e7fb0d1318851e32591b51a6bc600c2d', 1102, '712dfd307e9f735a037e1391f16c8747e7fb0d1318851e32591b51a6bc600c2d', 46800, 46800, '');

INSERT INTO manifest_blob_refs (repo_id, digest, blob_id) VALUES (1, 'sha256:8a9217f1887083297faf37cb2c1808f71289f0cd722d6e5157a07be1c362945f', 1);

INSERT INTO manifests (repo_id, digest, media_type, size_bytes, pushed_at, validated_at, validation_error_message) VALUES (1, 'sha256:8a9217f1887083297faf37cb2c1808f71289f0cd722d6e5157a07be1c362945f', 'application/vnd.docker.distribution.manifest.v2+json', 1367, 46800, 90000, '');

INSERT INTO repos (id, account_name, name) VALUES (1, 'test1', 'foo');
