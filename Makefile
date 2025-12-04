.PHONY: build run docker-build docker-run docker-push clean test-api

build:
	go build -o fpga-compiler .

run:
	go run main.go

docker-build:
	docker build -t fpga-compiler:latest .

docker-run:
	docker run -p 8080:8080 --rm fpga-compiler:latest

docker-compose-up:
	docker-compose up --build

docker-compose-down:
	docker-compose down

clean:
	rm -f fpga-compiler
	docker-compose down -v

test-api:
	@echo "Testing API endpoint..."
	@jq -n --rawfile verilog test/tt_um_factory_test.v \
		'{sources: {"project.v": $$verilog}, topModule: "tt_um_factory_test"}' | \
	curl -s -X POST http://localhost:8080/api/compile \
		-H "Content-Type: application/json" \
		-d @- > /tmp/test-api-response.txt
	@echo "Validating response..."
	@grep -q '^data: ' /tmp/test-api-response.txt || (echo "ERROR: No events received from API" && exit 1)
	@grep -q '"type":"success"' /tmp/test-api-response.txt || (echo "ERROR: No success event found" && exit 1)
	@grep -q '"data":"base64:' /tmp/test-api-response.txt || (echo "ERROR: No base64 bitstream found" && exit 1)
	@echo "✓ Test passed: API returned success with bitstream"
