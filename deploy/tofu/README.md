# OpenTofu: the off-site backup bucket

An optional starting point, not a turnkey cloud deploy. The primary way to run
Censurado is the Docker Compose stack in [../README.md](../README.md). This module
provisions the one piece of cloud infrastructure that is genuinely portable: the
S3-compatible bucket the Litestream sidecar replicates the database to.

CDN and compute are deliberately not here. They are the most vendor-specific part
of any deployment (every CDN has a different resource model), so wiring them is
left to you; the static site just needs to be served from the `site-data` volume
behind whatever CDN you run, with the cache policy in
[../CACHING.md](../CACHING.md).

## Use

Credentials come from the environment, never from a committed file.

```
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...

cd deploy/tofu
tofu init
tofu plan  -var backup_bucket=censurado-backups
tofu apply -var backup_bucket=censurado-backups
```

For an S3-compatible store (Cloudflare R2, Backblaze B2, MinIO) also pass the
endpoint and a region:

```
tofu apply \
  -var backup_bucket=censurado-backups \
  -var region=auto \
  -var s3_endpoint=https://<accountid>.r2.cloudflarestorage.com
```

Then put the outputs into `deploy/.env` as `LITESTREAM_S3_BUCKET`,
`LITESTREAM_S3_REGION`, and `LITESTREAM_S3_ENDPOINT`, and uncomment the s3 block in
`deploy/litestream.yml`. After any change to the backup target, run the restore
drill (`scripts/restore-drill.sh`) against it: a bucket that exists is not a
backup until a restore from it passes.

## What it creates

A single private, versioned bucket. Versioning keeps prior generations so a bad
replication cannot silently overwrite a good backup; the public-access block keeps
the database off the open internet. Nothing else: no CDN, no compute, no DNS.
