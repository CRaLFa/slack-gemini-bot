#!/bin/bash

gcloud functions deploy slack-gemini \
	--gen2 \
	--runtime=go122 \
	--region=asia-northeast1 \
	--source=. \
	--entry-point=SlackGemini \
	--trigger-http \
	--allow-unauthenticated \
	--env-vars-file .env.yaml
