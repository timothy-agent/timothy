# syntax=docker/dockerfile:1

# Node environment variant (tag timothy-sandbox-node): node/npm already
# live in the base image (needed there for the headless claude CLI
# executor itself), so this variant is a no-op tag over base — it
# exists purely so "node" is a selectable environment key.
FROM timothy-sandbox-base:latest
