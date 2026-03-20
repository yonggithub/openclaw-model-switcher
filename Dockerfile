FROM alpine:latest

RUN apk add --no-cache procps

COPY build/OpenClawSwitch-linux /usr/local/bin/OpenClawSwitch
RUN chmod +x /usr/local/bin/OpenClawSwitch

WORKDIR /data

EXPOSE 8356

ENTRYPOINT ["OpenClawSwitch"]
