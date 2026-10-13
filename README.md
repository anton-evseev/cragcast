# cragcast

## Build and publish the Docker image

Requires Git, Docker with Buildx, a builder that supports AMD64 and ARM64, and write access to the Docker Hub repository. From a repository checkout, run:

```sh
docker login
./publish-docker.sh
```

The script publishes AMD64 and ARM64 images to `antonevseev/cragcast`, tagged `latest` and with the current Git commit ID. Set `IMAGE` to override the repository name, without a tag.

## Publish static pages

Install Docker and copy `publish-cloudflare.sh` to the machine running the scheduled task. The task's account needs access to Docker and a writable working directory. Building the image on that machine is not required.

Create a Cloudflare API token with **Workers Editor** access scoped to the existing `cragcast` Worker. Do not put the token in the repository.

Set these variables in the environment of the scheduled task:

- `IMAGE`: Docker image name and tag, such as `antonevseev/cragcast:latest`.
- `WORK_DIR`: writable directory for each run's files.
- `CLOUDFLARE_API_TOKEN`: Cloudflare API token.
- `CLOUDFLARE_ACCOUNT_ID`: Cloudflare account ID.

`REGION` is optional and defaults to `seattle`.

Run the script on a schedule with these variables set:

```sh
sh /path/to/publish-cloudflare.sh
```

Each run pulls the image, scrapes the forecast, bundles the static pages, and deploys them through the Cloudflare API. If a command fails, the script skips the remaining steps and exits with an error after cleanup. With `latest`, new image releases are picked up automatically. The run directory is removed after successful and failed runs.

To deploy an existing bundle directly, set the Cloudflare variables and run:

```sh
cragcast deploy-cloudflare --from /path/to/bundle
```

The Worker name defaults to `cragcast`; use `--worker` to override it. Deployment uses HTML routing and the bundled `404.html` page. No Wrangler installation or configuration file is required.

When upgrading from the Wrangler image, replace the publish script and image together before re-enabling the scheduled task.
