FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=$(date +%y%m%d)" -o /voidrift ./cmd/voidrift \
 && CGO_ENABLED=0 go build -ldflags "-X main.version=$(date +%y%m%d)" -o /drifter ./cmd/drifter \
 && mkdir /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /voidrift /drifter /
COPY --from=build --chown=65532:65532 /data /data
WORKDIR /data
VOLUME /data
ENTRYPOINT ["/voidrift"]
# Configure with VOIDRIFT_* environment variables or flags. Data files default
# to the working directory (/data). Run drifter with --entrypoint /drifter.
