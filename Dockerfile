# ai-form-backend - AGPL-3.0
# 单二进制镜像:前端控制台已 go:embed,无需 node 阶段。
# 提示词(prompts/private)是私有运行时配置,绝不打进镜像,运行时挂载。
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-form-backend .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=build /out/ai-form-backend .
USER app
EXPOSE 8090
ENTRYPOINT ["/app/ai-form-backend"]
