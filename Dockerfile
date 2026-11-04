FROM python:3.13-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /srv
RUN mkdir /data && chown 10001:10001 /data
COPY app ./app
LABEL org.opencontainers.image.source="https://github.com/railadeividas/iptv-proxy"
ARG VERSION=dev
ENV APP_VERSION=$VERSION
USER 10001:10001
EXPOSE 3333
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:3333/healthz', timeout=3).read()"
CMD ["python", "-m", "app"]
