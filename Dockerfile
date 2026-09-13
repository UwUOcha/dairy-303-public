# Universal core image: raspd, botd and webd (including admin).

FROM golang:1.27-alpine AS build

WORKDIR /src
ARG SOURCE_CODE_URL=https://github.com/UwUOcha/dairy-303-public

# Зависимости отдельным слоем: пересобираются только при правке go.mod/go.sum,
# а не на каждое изменение кода.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO выключен намеренно: sqlite здесь чистый Go (modernc), так что в образе
# не нужно ни libc, ни единой системной библиотеки.
# Страница панели (html, css, js) уезжает внутрь webd через embed: отдельного
# слоя со статикой и тома под неё не нужно, а версия страницы всегда совпадает
# с версией бинаря.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/UwUOcha/dairy-303-public/internal/buildinfo.SourceCodeURL=${SOURCE_CODE_URL}" -o /out/raspd  ./cmd/raspd && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/UwUOcha/dairy-303-public/internal/buildinfo.SourceCodeURL=${SOURCE_CODE_URL}" -o /out/botd   ./cmd/botd && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/UwUOcha/dairy-303-public/internal/buildinfo.SourceCodeURL=${SOURCE_CODE_URL}" -o /out/webd   ./cmd/webd

# Каталоги под базу и сокет готовим здесь: в финальном образе нет шелла, а
# докер при первом монтировании пустого тома копирует владельца и права с
# каталога в образе. Без этого том достался бы root, и процесс под nonroot не
# смог бы в него писать.
RUN install -d -m 0750 -o 65532 -g 65532 /stage/var/lib/rasp /stage/run/rasp /stage/var/lib/admin

# distroless: ни шелла, ни пакетного менеджера, ни утилит — эксплуатировать
# внутри контейнера просто нечего. Из полезного есть корневые сертификаты,
# нужные для HTTPS к вузу и к площадкам.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /stage/ /
COPY --from=build /out/raspd /out/botd /out/webd /usr/local/bin/

USER nonroot:nonroot
WORKDIR /var/lib/rasp
