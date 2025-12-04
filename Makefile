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
	@curl -X POST http://localhost:8080/api/compile \
		-H "Content-Type: application/json" \
		-d '{"sources":{"project.v":"module tt_um_test(input clk, output led); reg r; always @(posedge clk) r <= ~r; assign led = r; endmodule"},"topModule":"tt_um_test"}'
