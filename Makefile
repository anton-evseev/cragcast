REGION ?= seattle
DATA_DIR ?= ./data
ADDR ?= 127.0.0.1:8090

.PHONY: preview
preview:
	go run . scrape --region "$(REGION)" --out "$(DATA_DIR)"
	go run . serve --region "$(REGION)" --from "$(DATA_DIR)" --addr "$(ADDR)"
