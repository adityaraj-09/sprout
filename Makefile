.PHONY: build server lab-primary lab-primary-stop clean reset-data

export PATH := /opt/homebrew/bin:/usr/local/bin:$(PATH)

build:
	go build -o bin/ardent ./cmd/ardent
	go build -o bin/ardent-server ./cmd/ardent-server

server: build
	./bin/ardent-server

lab-primary:
	bash scripts/lab-primary.sh

lab-primary-stop:
	bash scripts/lab-primary.sh stop

clean:
	-pg_ctl -D data/main stop -m fast 2>/dev/null || true
	-pg_ctl -D data/lab-primary stop -m fast 2>/dev/null || true
	@for d in data/branches/*; do \
	  [ -d "$$d" ] && pg_ctl -D "$$d" stop -m fast 2>/dev/null || true; \
	done

reset-data: clean
	rm -rf data/main data/branches data/snapshots data/control.json data/meta.json
	@echo "kept lab-primary if present; wipe with: rm -rf data/lab-primary"
