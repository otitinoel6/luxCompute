# Force Go 1.22 to fix library errors
FROM golang:1.22-alpine
WORKDIR /app

COPY go.* ./
COPY main.go ./
COPY index.html ./

RUN go mod download
RUN go build -o main .

# Run
CMD ["/app/main"]
