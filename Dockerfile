##############################################################
## Stage 1 - Go Build
##############################################################

# Using a specific version of golang based on alpine for building the application
FROM golang:1.22.3-alpine AS builder

RUN apk add wget
RUN apk add --no-cache sqlite-libs sqlite-dev build-base
RUN mkdir -p /util
WORKDIR /util
RUN wget https://github.com/gobuffalo/cli/releases/download/v0.18.14/buffalo_0.18.14_Linux_x86_64.tar.gz \
    && tar -xvzf buffalo_0.18.14_Linux_x86_64.tar.gz \
    && mv buffalo /usr/local/bin/buffalo \
    && rm buffalo_0.18.14_Linux_x86_64.tar.gz

RUN mkdir -p /src/mc-iam-manager
WORKDIR /src/mc-iam-manager

ENV GOPROXY http://proxy.golang.org
# ENV GO111MODULE on

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

ADD . .
RUN buffalo build --static -o /bin/app

#############################################################
## Stage 2 - Application Deploy
##############################################################

FROM debian:buster-slim

WORKDIR /bin/
COPY --from=builder /bin/app .

# Uncomment to run the binary in "production" mode:
# ENV GO_ENV=production

# Bind the app to 0.0.0.0 so it can be seen from outside the container
ENV ADDR=0.0.0.0 \
    PORT=3000

ENV DEV_DATABASE_URL=postgres://mciamadmin:password@postgres:5432/mciamdb \
    DATABASE_URL=postgres://mciamadmin:password@postgres:5432/mciamdb 

ENV CBLOG_ROOT=/bin \
    MCIAMMANAGER_ROOT=/bin

EXPOSE 3000

# Uncomment to run the migrations before running the binary:
# CMD /bin/app migrate; /bin/app
CMD bash -c 'until /bin/app migrate; do echo "Migration failed. Retrying in 10 seconds..."; sleep 10; done; /bin/app'
# CMD exec /bin/app
