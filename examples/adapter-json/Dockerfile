FROM python:3.13-alpine
WORKDIR /app
COPY examples/adapter-json/server.py examples/adapter-json/data.json ./
USER 65532:65532
ENV ADAPTER_HOST=0.0.0.0
EXPOSE 8310
CMD ["python", "server.py"]
