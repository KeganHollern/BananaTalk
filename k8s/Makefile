.PHONY: dry-run deploy

dry-run:
	kubectl apply -k base --dry-run=client -o yaml

deploy:
	kubectl apply -k base
