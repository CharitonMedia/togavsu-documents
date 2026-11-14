# Documents and templates module image.
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/documents ./cmd/documents

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/documents /documents
EXPOSE 8080
ENTRYPOINT ["/documents", "serve"]
