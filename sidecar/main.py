"""Python sidecar for the Go gateway.

Exposes the skill registry, vector memory, and history store as a small
HTTP service that the Go gateway calls.  The Python source for ``skills``,
``memory`` and ``llm`` is vendored under ``sidecar/python/`` (mounted at
``/app`` in the container), making this a self-contained service.
"""
from __future__ import annotations

import logging
import os
from contextlib import asynccontextmanager
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from skills.registry import SkillRegistry
from skills.weather import WeatherSkill
from skills.datetime_skill import DateTimeSkill
from skills.http_request import HttpRequestSkill
from skills.shell_command import ShellCommandSkill
from skills.web_scrape import WebScrapeSkill
from skills.dns_lookup import DnsLookupSkill
from skills.cron_schedule import CronScheduleSkill
from skills.ping_check import PingCheckSkill
from skills.ssh_command import SshCommandSkill
from skills.port_scan import PortScanSkill
from skills.http_health import HttpHealthSkill
from skills.whois_lookup import WhoisLookupSkill
from skills.base64_skill import Base64Skill
from skills.hash_skill import HashSkill
from skills.math_eval import MathEvalSkill
from skills.ip_info import IpInfoSkill
from skills.uuid_gen import UuidGenSkill
from skills.password_gen import PasswordGenSkill
from skills.qr_generate import QrGenerateSkill
from skills.cidr_calc import CidrCalcSkill
from skills.skill_creator import SkillCreatorSkill
from skills.markdown_skill import load_markdown_skills

from memory.history_store import HistoryStore
from memory.vector_store import VectorStore
from llm.embeddings_client import OllamaEmbeddingsClient
from llm.openai_client import OpenAIEmbeddingsClient

logger = logging.getLogger("sidecar")
logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(name)s] %(levelname)s: %(message)s")


def _build_embeddings():
    """Pick the embeddings backend the same way the original gateway did."""
    provider = os.environ.get("LLM_PROVIDER", "ollama").lower()
    if provider in ("openai", "openai-codex", "codex"):
        api_key = os.environ.get("OPENAI_API_KEY", "")
        if not api_key:
            raise RuntimeError("OPENAI_API_KEY required when LLM_PROVIDER=openai*")
        return OpenAIEmbeddingsClient(
            api_key=api_key,
            model=os.environ.get("OPENAI_EMBED_MODEL", "text-embedding-3-small"),
            base_url=os.environ.get("OPENAI_BASE_URL", "https://api.openai.com/v1"),
            timeout=float(os.environ.get("OPENAI_TIMEOUT", "120")),
        )
    return OllamaEmbeddingsClient(
        os.environ.get("OLLAMA_URL", "http://127.0.0.1:11434"),
        os.environ.get("OLLAMA_EMBED_MODEL", "nomic-embed-text"),
        timeout=float(os.environ.get("OLLAMA_TIMEOUT", "120")),
    )


def _build_skill_registry() -> SkillRegistry:
    """Register every Python skill EXCEPT schedule_task.

    schedule_task lives on the Go side because the scheduler store is owned
    by the Go scheduler runner.  The Go agent loop registers it natively.
    """
    skills = SkillRegistry()
    skills.register(WeatherSkill())
    skills.register(DateTimeSkill())
    skills.register(HttpRequestSkill(timeout=float(os.environ.get("SKILL_HTTP_TIMEOUT", "30"))))
    skills.register(ShellCommandSkill(enabled=os.environ.get("SKILL_SHELL_ENABLED", "false").lower() in ("true", "1", "yes")))
    skills.register(WebScrapeSkill(timeout=float(os.environ.get("SKILL_HTTP_TIMEOUT", "15"))))
    skills.register(DnsLookupSkill())
    skills.register(CronScheduleSkill())
    skills.register(PingCheckSkill())
    skills.register(PortScanSkill())
    skills.register(HttpHealthSkill())
    skills.register(WhoisLookupSkill())
    skills.register(Base64Skill())
    skills.register(HashSkill())
    skills.register(MathEvalSkill())
    skills.register(IpInfoSkill(timeout=float(os.environ.get("SKILL_HTTP_TIMEOUT", "10"))))
    skills.register(UuidGenSkill())
    skills.register(PasswordGenSkill())
    skills.register(QrGenerateSkill())
    skills.register(CidrCalcSkill())
    skills.register(SshCommandSkill(
        enabled=os.environ.get("SKILL_SSH_ENABLED", "false").lower() in ("true", "1", "yes"),
        default_identity_file=os.environ.get("SKILL_SSH_IDENTITY_FILE", ""),
        default_user=os.environ.get("SKILL_SSH_DEFAULT_USER", ""),
        private_key_content=os.environ.get("SKILL_SSH_PRIVATE_KEY", ""),
    ))

    # Late-binding executor for pipeline skills + skill_creator hot-registration.
    def skill_executor(skill_name: str, args: dict):
        s = skills.get(skill_name)
        if not s:
            return {"error": f"Skill '{skill_name}' not found"}
        return s.execute(**(args or {}))

    skills_dir = os.environ.get("SKILLS_DIR", "/app/skills")
    for md_skill in load_markdown_skills(skills_dir, skill_executor=skill_executor):
        skills.register(md_skill)

    skills.register(SkillCreatorSkill(
        skills_dir=skills_dir,
        skill_registry=skills,
        skill_executor=skill_executor,
    ))
    return skills


@asynccontextmanager
async def lifespan(app: FastAPI):
    data_dir = os.environ.get("DATA_DIR", "/app/data")
    sqlite_path = os.environ.get("AGENT_DB", os.path.join(data_dir, "agent_memory.sqlite3"))
    vector_dir = os.environ.get("AGENT_VECTOR_DIR", os.path.join(data_dir, "vector_db"))

    app.state.skills = _build_skill_registry()
    app.state.history = HistoryStore(sqlite_path)
    app.state.vector = VectorStore(
        persist_dir=vector_dir,
        collection_name=os.environ.get("AGENT_VECTOR_COLLECTION", "memories"),
        embeddings_client=_build_embeddings(),
        dedup_distance=float(os.environ.get("AGENT_DEDUP_DISTANCE", "0.08")),
    )
    logger.info("sidecar ready (skills=%d)", len(app.state.skills.list_names()))
    yield


app = FastAPI(title="OmegaGrid Sidecar", lifespan=lifespan)


@app.get("/skills")
def list_skills() -> Dict[str, Any]:
    """Return every registered skill, normalized to the schema the Go gateway expects.

    The Go side wants ``parameters`` to be a flat dict of ``{name -> {type, description, required}}``
    and an optional ``body`` (markdown skill prompt instructions).
    """
    skills_out: List[Dict[str, Any]] = []
    for s in app.state.skills._skills.values():  # noqa: SLF001 — internal access is fine in-process
        params: Dict[str, Dict[str, Any]] = {}
        for name, p in (s.parameters or {}).items():
            if isinstance(p, dict):
                params[name] = {
                    "type": str(p.get("type", "string")),
                    "description": str(p.get("description", "")),
                    "required": bool(p.get("required", False)),
                }
            else:
                params[name] = {"type": "string", "description": "", "required": False}
        skills_out.append({
            "name": s.name,
            "description": s.description,
            "parameters": params,
            "body": getattr(s, "body", "") or "",
        })
    return {"skills": skills_out}


class ExecuteRequest(BaseModel):
    name: str
    args: Dict[str, Any] = Field(default_factory=dict)


@app.post("/skills/execute")
def execute_skill(req: ExecuteRequest) -> Dict[str, Any]:
    skill = app.state.skills.get(req.name)
    if not skill:
        raise HTTPException(status_code=404, detail=f"Skill '{req.name}' not found")
    try:
        result = skill.execute(**(req.args or {}))
    except TypeError as e:
        # The LLM occasionally passes unknown kwargs — surface them as a soft
        # error rather than a 500 so the agent loop can recover.
        return {"result": {"error": f"bad arguments: {e}"}}
    except Exception as e:
        logger.exception("skill %s failed", req.name)
        return {"result": {"error": str(e), "skill": req.name}}
    return {"result": result}


class MemoryAddRequest(BaseModel):
    text: str
    meta: Dict[str, Any] = Field(default_factory=dict)


@app.post("/memory/add")
def memory_add(req: MemoryAddRequest) -> Dict[str, Any]:
    try:
        return app.state.vector.add_text(req.text, req.meta)
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


class MemorySearchRequest(BaseModel):
    query: str
    k: int = 5


@app.post("/memory/search")
def memory_search(req: MemorySearchRequest) -> Dict[str, Any]:
    try:
        hits, timings = app.state.vector.search_with_timings(req.query, k=req.k)
        return {"hits": hits, "timings": timings}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/sessions/new")
def new_session() -> Dict[str, Any]:
    return {"session_id": app.state.history.create_session()}


@app.get("/sessions")
def list_sessions(limit: int = 50) -> Dict[str, Any]:
    return {"sessions": app.state.history.list_sessions(limit=limit)}


@app.get("/sessions/{session_id}/messages")
def session_messages(session_id: int, limit: int = 200, offset: int = 0) -> Dict[str, Any]:
    try:
        return {
            "session_id": session_id,
            "messages": app.state.history.list_messages(session_id=session_id, limit=limit, offset=offset),
        }
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.get("/sessions/{session_id}/tail")
def session_tail(session_id: int, limit: int = 30) -> Dict[str, Any]:
    """Used by the Go agent loop to seed the LLM context window."""
    try:
        return {"messages": app.state.history.load_tail(session_id=session_id, limit=limit)}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


class AddMessageRequest(BaseModel):
    role: str
    content: Any


@app.post("/sessions/{session_id}/messages")
def add_message(session_id: int, req: AddMessageRequest) -> Dict[str, Any]:
    try:
        app.state.history.add_message(session_id, req.role, req.content)
        return {"ok": True}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.get("/health")
def health() -> Dict[str, Any]:
    return {
        "ok": True,
        "skill_count": len(app.state.skills.list_names()) if hasattr(app.state, "skills") else 0,
    }
