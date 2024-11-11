#!/bin/bash

set -eu

. set_env.sh

deploy_pub () {
	(cd pub && go mod tidy)
	gcloud functions deploy slack-gemini-pub \
		--gen2 \
		--runtime=go122 \
		--region=asia-northeast1 \
		--source=./pub \
		--entry-point=Publish \
		--trigger-http \
		--allow-unauthenticated \
		--env-vars-file=./.env.yaml \
		--service-account="$SERVICE_ACCOUNT"
}

deploy_sub () {
	(cd sub && go mod tidy)
	gcloud functions deploy slack-gemini-sub \
		--gen2 \
		--runtime=go122 \
		--region=asia-northeast1 \
		--source=./sub \
		--entry-point=Subscribe \
		--trigger-topic=slack-gemini \
		--env-vars-file=./.env.yaml \
		--service-account="$SERVICE_ACCOUNT"
}

main () {
	cd "$(dirname "$0")"
	[[ $# -lt 1 || "$1" = 'pub' ]] && {
		echo -e 'Start deploying slack-gemini-pub function...\n'
		deploy_pub
		echo
	}
	[[ $# -lt 1 || "$1" = 'sub' ]] && {
		echo -e 'Start deploying slack-gemini-sub function...\n'
		deploy_sub
	}
}

main "$@"
