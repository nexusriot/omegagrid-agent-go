# Python sidecar — fully self-contained.  All Python source code is vendored
# under sidecar/python/ in this repo, so the build does NOT depend on any
# sibling project.
FROM python:3.11-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
        jq ripgrep openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Install Python deps (fastapi + chromadb + everything the skills need).
COPY sidecar/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -U pip && pip install --no-cache-dir -r /tmp/requirements.txt

# Vendored Python packages: skills, memory, llm.  These are the only modules
# the sidecar imports — schedule_task and the gateway/scheduler/integrations
# packages from the original project have been rewritten in Go and are not
# needed here.
COPY sidecar/python/skills /app/skills
COPY sidecar/python/memory /app/memory
COPY sidecar/python/llm    /app/llm

# Sidecar shim that exposes everything over HTTP.
COPY sidecar/main.py /app/sidecar_main.py

ENV PYTHONUNBUFFERED=1 PYTHONPATH=/app SKILLS_DIR=/app/skills
EXPOSE 8001

CMD ["uvicorn", "sidecar_main:app", "--host", "0.0.0.0", "--port", "8001"]
