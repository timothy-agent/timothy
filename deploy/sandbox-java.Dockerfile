# syntax=docker/dockerfile:1

# Java environment variant (tag timothy-sandbox-java): adds a JDK and
# Maven to the base mission sandbox.
FROM eclipse-temurin:21-jdk AS java-dist

ARG SANDBOX_BASE=timothy-sandbox-base:latest
FROM ${SANDBOX_BASE}

USER root

COPY --from=java-dist /opt/java/openjdk /opt/java/openjdk
ENV JAVA_HOME=/opt/java/openjdk
# Same lesson as the go variant: sandboxd's fixed exec PATH ignores
# image ENV, so java/javac/jar must be symlinked into /usr/local/bin,
# not just added to PATH.
RUN ln -s /opt/java/openjdk/bin/java /usr/local/bin/java \
    && ln -s /opt/java/openjdk/bin/javac /usr/local/bin/javac \
    && ln -s /opt/java/openjdk/bin/jar /usr/local/bin/jar

RUN apt-get update && apt-get install -y --no-install-recommends \
    maven \
    && rm -rf /var/lib/apt/lists/*

USER 65534:65534
