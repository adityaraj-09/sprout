.PHONY: build server lab-primary lab-primary-stop clean reset-data npm npm-link

export PATH := /opt/homebrew/bin:/usr/local/bin:$(PATH)

build:
	go build -o bin/sprout ./cmd/sprout
	go build -o bin/sprout-server ./cmd/sprout-server

server: build
	./bin/sprout-server

lab-primary:
	bash scripts/lab-primary.sh

lab-primary-stop:
	bash scripts/lab-primary.sh stop

npm:
	cd npm && npm install && npm run build

npm-link: npm
	cd npm && npm link

clean:
	-pg_ctl -D data/main stop -m fast 2>/dev/null || true
	-pg_ctl -D data/lab-primary stop -m fast 2>/dev/null || true
	@for d in data/replicas/* data/branches/*; do \
	  [ -d "$$d" ] && pg_ctl -D "$$d" stop -m fast 2>/dev/null || true; \
	done

reset-data: clean
	rm -rf data/main data/replicas data/branches data/snapshots data/control.json data/control.db data/control.db-* data/meta.json
	@echo "kept lab-primary if present; wipe with: rm -rf data/lab-primary"
