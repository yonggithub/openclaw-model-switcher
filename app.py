#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
OpenClawSwitch Web UI — Flask 后端
管理 AI API 服务商配置，查询模型，生成 openclaw.json 配置。
"""

import json
import sqlite3
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import requests as http_requests
from flask import Flask, g, jsonify, render_template, request

app = Flask(__name__)

BASE_DIR = Path(__file__).resolve().parent
DB_PATH = BASE_DIR / "openclawswitch.db"
DEFAULT_CONFIG_PATH = str(BASE_DIR / "openclaw.json")

CONFIG_PATH_STORE = {"path": DEFAULT_CONFIG_PATH}

MODEL_TEMPLATE = {
    "reasoning": False,
    "input": ["text"],
    "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
    "contextWindow": 200000,
    "maxTokens": 8192,
}


# ---------------------------------------------------------------------------
# Database helpers
# ---------------------------------------------------------------------------

def get_db() -> sqlite3.Connection:
    if "db" not in g:
        g.db = sqlite3.connect(str(DB_PATH))
        g.db.row_factory = sqlite3.Row
        g.db.execute("PRAGMA journal_mode=WAL")
        g.db.execute("PRAGMA foreign_keys=ON")
    return g.db


@app.teardown_appcontext
def close_db(_exc):
    db = g.pop("db", None)
    if db is not None:
        db.close()


def init_db():
    conn = sqlite3.connect(str(DB_PATH))
    conn.executescript("""
        CREATE TABLE IF NOT EXISTS providers (
            id         INTEGER PRIMARY KEY AUTOINCREMENT,
            name       TEXT    NOT NULL UNIQUE,
            base_url   TEXT    NOT NULL,
            api_key    TEXT    DEFAULT '',
            api_type   TEXT    DEFAULT 'openai-completions',
            created_at TEXT    DEFAULT (datetime('now'))
        );
        CREATE TABLE IF NOT EXISTS models (
            id          INTEGER PRIMARY KEY AUTOINCREMENT,
            provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
            model_id    TEXT    NOT NULL,
            owned_by    TEXT    DEFAULT '',
            selected    INTEGER DEFAULT 0,
            created_at  TEXT    DEFAULT (datetime('now')),
            UNIQUE(provider_id, model_id)
        );
        CREATE TABLE IF NOT EXISTS settings (
            key   TEXT PRIMARY KEY,
            value TEXT NOT NULL
        );
    """)
    # John Tang: 新增 - 从 DB 加载持久化配置路径，无记录时写入默认值。20260319
    row = conn.execute("SELECT value FROM settings WHERE key = 'config_path'").fetchone()
    if row:
        CONFIG_PATH_STORE["path"] = row[0]
    else:
        conn.execute(
            "INSERT INTO settings (key, value) VALUES ('config_path', ?)",
            (DEFAULT_CONFIG_PATH,),
        )
        conn.commit()
    conn.close()


# ---------------------------------------------------------------------------
# Utility — fetch models from a provider API
# ---------------------------------------------------------------------------

def _build_models_url(base_url: str) -> str:
    base = base_url.rstrip("/")
    if base.endswith("/v1"):
        return f"{base}/models"
    return f"{base}/v1/models"


def fetch_models_from_provider(base_url: str, api_key: str) -> list[dict]:
    url = _build_models_url(base_url)
    data = None
    last_err = ""

    auth_methods = [
        ("Bearer", {"Authorization": f"Bearer {api_key}"}),
        ("x-api-key", {"x-api-key": api_key}),
    ]
    if not api_key:
        auth_methods = [("NoAuth", {})]

    for auth_name, headers in auth_methods:
        try:
            r = http_requests.get(url, headers=headers, timeout=30)
            if r.status_code == 200:
                data = r.json()
                break
            last_err = f"{auth_name} 返回 {r.status_code}"
        except http_requests.RequestException as e:
            last_err = f"{auth_name}: {e}"

    if data is None:
        raise RuntimeError(f"无法获取模型列表: {last_err}")

    if isinstance(data, list):
        raw = data
    elif isinstance(data, dict) and "data" in data:
        raw = data["data"]
    else:
        raise ValueError(f"无法解析响应，期望 list 或 dict.data，得到 {type(data)}")

    result = []
    for m in raw:
        if not isinstance(m, dict):
            continue
        mid = m.get("id") or m.get("model") or m.get("name")
        if not mid or not isinstance(mid, str):
            continue
        owned = m.get("owned_by", "") or ""
        result.append({"model_id": mid, "owned_by": owned})
    return result


# ---------------------------------------------------------------------------
# Utility — apply config to openclaw.json
# ---------------------------------------------------------------------------

def apply_config_to_file(primary: str, fallbacks: list[str]):
    config_path = Path(CONFIG_PATH_STORE["path"])
    if not config_path.is_file():
        raise FileNotFoundError(f"配置文件不存在: {config_path}")

    config = json.loads(config_path.read_text(encoding="utf-8"))

    db = get_db()
    selected_rows = db.execute("""
        SELECT m.model_id, m.owned_by, p.name AS provider_name,
               p.base_url, p.api_key, p.api_type
        FROM models m
        JOIN providers p ON m.provider_id = p.id
        WHERE m.selected = 1
        ORDER BY p.name, m.model_id
    """).fetchall()

    # 1) 清空并重建 models.providers
    config.setdefault("models", {})
    config["models"].setdefault("mode", "replace")
    config["models"]["providers"] = {}

    providers_map: dict[str, dict] = {}
    for row in selected_rows:
        pname = row["provider_name"]
        if pname not in providers_map:
            providers_map[pname] = {
                "baseUrl": row["base_url"],
                "apiKey": row["api_key"],
                "api": row["api_type"],
                "models": [],
            }
        providers_map[pname]["models"].append({
            "id": row["model_id"],
            "name": row["model_id"],
            **MODEL_TEMPLATE,
        })

    config["models"]["providers"] = providers_map

    # 2) 清空并重建 agents.defaults.models
    config.setdefault("agents", {})
    config["agents"].setdefault("defaults", {})
    defaults = config["agents"]["defaults"]

    defaults["models"] = {}
    for row in selected_rows:
        key = f"{row['provider_name']}/{row['model_id']}"
        defaults["models"][key] = {}

    # 3) 清空并重建 agents.defaults.model.primary / fallbacks
    defaults.setdefault("model", {})
    defaults["model"]["primary"] = primary
    defaults["model"]["fallbacks"] = fallbacks

    # 备份并写入
    backup = config_path.parent / f"openclaw.json.bak.{time.strftime('%Y%m%d%H%M%S')}"
    backup.write_text(config_path.read_text(encoding="utf-8"), encoding="utf-8")
    config_path.write_text(json.dumps(config, ensure_ascii=False, indent=2), encoding="utf-8")

    return {
        "providers_count": len(providers_map),
        "models_count": len(selected_rows),
        "primary": primary,
        "fallbacks": fallbacks,
        "backup": str(backup),
    }


# ---------------------------------------------------------------------------
# Routes — pages
# ---------------------------------------------------------------------------

@app.route("/")
def index():
    return render_template("index.html")


# ---------------------------------------------------------------------------
# Routes — providers CRUD
# ---------------------------------------------------------------------------

@app.route("/api/providers", methods=["GET"])
def list_providers():
    db = get_db()
    rows = db.execute("""
        SELECT p.*, COUNT(m.id) AS model_count,
               SUM(CASE WHEN m.selected = 1 THEN 1 ELSE 0 END) AS selected_count
        FROM providers p
        LEFT JOIN models m ON m.provider_id = p.id
        GROUP BY p.id
        ORDER BY p.created_at
    """).fetchall()
    return jsonify([dict(r) for r in rows])


@app.route("/api/providers", methods=["POST"])
def create_provider():
    data = request.json or {}
    name = (data.get("name") or "").strip()
    base_url = (data.get("base_url") or "").strip()
    api_key = (data.get("api_key") or "").strip()
    api_type = (data.get("api_type") or "openai-completions").strip()

    if not name or not base_url:
        return jsonify({"error": "服务商名称和 Base URL 为必填项"}), 400

    db = get_db()
    try:
        db.execute(
            "INSERT INTO providers (name, base_url, api_key, api_type) VALUES (?, ?, ?, ?)",
            (name, base_url, api_key, api_type),
        )
        db.commit()
    except sqlite3.IntegrityError:
        return jsonify({"error": f"服务商 '{name}' 已存在"}), 409

    return jsonify({"ok": True}), 201


@app.route("/api/providers/<int:pid>", methods=["PUT"])
def update_provider(pid: int):
    data = request.json or {}
    db = get_db()
    row = db.execute("SELECT id FROM providers WHERE id = ?", (pid,)).fetchone()
    if not row:
        return jsonify({"error": "服务商不存在"}), 404

    fields, values = [], []
    for col in ("name", "base_url", "api_key", "api_type"):
        if col in data:
            fields.append(f"{col} = ?")
            values.append(data[col])
    if not fields:
        return jsonify({"error": "没有需要更新的字段"}), 400

    values.append(pid)
    try:
        db.execute(f"UPDATE providers SET {', '.join(fields)} WHERE id = ?", values)
        db.commit()
    except sqlite3.IntegrityError:
        return jsonify({"error": "服务商名称重复"}), 409
    return jsonify({"ok": True})


@app.route("/api/providers/<int:pid>", methods=["DELETE"])
def delete_provider(pid: int):
    db = get_db()
    db.execute("DELETE FROM providers WHERE id = ?", (pid,))
    db.commit()
    return jsonify({"ok": True})


# ---------------------------------------------------------------------------
# Routes — fetch models from provider API
# ---------------------------------------------------------------------------

@app.route("/api/providers/<int:pid>/fetch", methods=["POST"])
def fetch_provider_models(pid: int):
    db = get_db()
    prov = db.execute("SELECT * FROM providers WHERE id = ?", (pid,)).fetchone()
    if not prov:
        return jsonify({"error": "服务商不存在"}), 404

    try:
        models = fetch_models_from_provider(prov["base_url"], prov["api_key"])
    except Exception as e:
        return jsonify({"error": str(e)}), 502

    for m in models:
        db.execute("""
            INSERT INTO models (provider_id, model_id, owned_by)
            VALUES (?, ?, ?)
            ON CONFLICT(provider_id, model_id) DO UPDATE SET owned_by = excluded.owned_by
        """, (pid, m["model_id"], m["owned_by"]))
    db.commit()

    return jsonify({"ok": True, "count": len(models)})


# ---------------------------------------------------------------------------
# Routes — models
# ---------------------------------------------------------------------------

@app.route("/api/models", methods=["GET"])
def list_models():
    db = get_db()
    rows = db.execute("""
        SELECT m.*, p.name AS provider_name
        FROM models m
        JOIN providers p ON m.provider_id = p.id
        ORDER BY p.name, m.model_id
    """).fetchall()
    return jsonify([dict(r) for r in rows])


@app.route("/api/models/<int:mid>/toggle", methods=["PUT"])
def toggle_model(mid: int):
    db = get_db()
    db.execute("UPDATE models SET selected = 1 - selected WHERE id = ?", (mid,))
    db.commit()
    row = db.execute("SELECT selected FROM models WHERE id = ?", (mid,)).fetchone()
    return jsonify({"ok": True, "selected": bool(row["selected"]) if row else False})


@app.route("/api/models/batch-select", methods=["POST"])
def batch_select_models():
    data = request.json or {}
    ids = data.get("ids", [])
    selected = 1 if data.get("selected", True) else 0

    if not ids:
        return jsonify({"error": "缺少模型 ID 列表"}), 400

    db = get_db()
    placeholders = ",".join("?" * len(ids))
    db.execute(f"UPDATE models SET selected = ? WHERE id IN ({placeholders})", [selected] + ids)
    db.commit()
    return jsonify({"ok": True})


# ---------------------------------------------------------------------------
# Routes — test model availability
# ---------------------------------------------------------------------------

def _build_chat_url(base_url: str) -> str:
    base = base_url.rstrip("/")
    if base.endswith("/v1"):
        return f"{base}/chat/completions"
    return f"{base}/v1/chat/completions"


@app.route("/api/models/test", methods=["POST"])
def test_model():
    data = request.json or {}
    model_key = (data.get("model_key") or "").strip()
    if not model_key or "/" not in model_key:
        return jsonify({"error": "无效的模型标识"}), 400

    provider_name = model_key.split("/", 1)[0]
    model_id = model_key.split("/", 1)[1]

    db = get_db()
    prov = db.execute("SELECT * FROM providers WHERE name = ?", (provider_name,)).fetchone()
    if not prov:
        return jsonify({"ok": False, "error": f"服务商 '{provider_name}' 不存在", "latency_ms": 0})

    url = _build_chat_url(prov["base_url"])
    api_key = prov["api_key"] or ""
    payload = {
        "model": model_id,
        "messages": [{"role": "user", "content": "Return only OK."}],
        "max_tokens": 1,
        "stream": False,
    }

    auth_attempts = [
        ("Bearer", {"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"}),
        ("x-api-key", {"x-api-key": api_key, "Content-Type": "application/json"}),
    ]
    if not api_key:
        auth_attempts = [("NoAuth", {"Content-Type": "application/json"})]

    last_err = ""
    for auth_name, headers in auth_attempts:
        try:
            t0 = time.time()
            r = http_requests.post(url, json=payload, headers=headers, timeout=30)
            latency = int((time.time() - t0) * 1000)

            if r.status_code == 200:
                return jsonify({"ok": True, "latency_ms": latency, "status_code": 200})

            body = {}
            try:
                body = r.json()
            except Exception:
                pass
            err_msg = body.get("error", {}).get("message", "") if isinstance(body.get("error"), dict) else str(body.get("error", ""))
            last_err = err_msg or f"HTTP {r.status_code}"
        except http_requests.Timeout:
            return jsonify({"ok": False, "error": "请求超时 (30s)", "latency_ms": 30000})
        except http_requests.RequestException as e:
            last_err = str(e)

    return jsonify({"ok": False, "error": last_err, "latency_ms": 0})


def _test_single_model(provider_row: dict, model_id: str) -> dict:
    """在线程池中执行的单模型测试（不依赖 Flask 请求上下文）。"""
    url = _build_chat_url(provider_row["base_url"])
    api_key = provider_row["api_key"] or ""
    payload = {
        "model": model_id,
        "messages": [{"role": "user", "content": "Return only OK."}],
        "max_tokens": 1,
        "stream": False,
    }
    auth_attempts = [
        ("Bearer", {"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"}),
        ("x-api-key", {"x-api-key": api_key, "Content-Type": "application/json"}),
    ]
    if not api_key:
        auth_attempts = [("NoAuth", {"Content-Type": "application/json"})]

    model_key = f"{provider_row['name']}/{model_id}"
    last_err = ""
    for _, headers in auth_attempts:
        try:
            t0 = time.time()
            r = http_requests.post(url, json=payload, headers=headers, timeout=30)
            latency = int((time.time() - t0) * 1000)
            if r.status_code == 200:
                return {"model_key": model_key, "ok": True, "latency_ms": latency}
            body = {}
            try:
                body = r.json()
            except Exception:
                pass
            err = body.get("error", {}).get("message", "") if isinstance(body.get("error"), dict) else str(body.get("error", ""))
            last_err = err or f"HTTP {r.status_code}"
        except http_requests.Timeout:
            return {"model_key": model_key, "ok": False, "error": "请求超时 (30s)", "latency_ms": 30000}
        except http_requests.RequestException as e:
            last_err = str(e)

    return {"model_key": model_key, "ok": False, "error": last_err, "latency_ms": 0}


# John Tang: 新增 - 批量测试接口，并发测试指定服务商下所有模型。20260319
@app.route("/api/models/batch-test", methods=["POST"])
def batch_test_models():
    data = request.json or {}
    model_keys = data.get("model_keys", [])

    if not model_keys:
        return jsonify({"error": "model_keys 不能为空"}), 400

    db = get_db()
    providers_cache: dict[str, dict] = {}
    tasks: list[tuple[dict, str]] = []
    for key in model_keys:
        if "/" not in key:
            continue
        pname, mid = key.split("/", 1)
        if pname not in providers_cache:
            row = db.execute("SELECT * FROM providers WHERE name = ?", (pname,)).fetchone()
            providers_cache[pname] = dict(row) if row else None
        prov = providers_cache[pname]
        if prov:
            tasks.append((prov, mid))

    results = []
    with ThreadPoolExecutor(max_workers=min(8, len(tasks) or 1)) as pool:
        futures = {pool.submit(_test_single_model, prov, mid): (prov, mid) for prov, mid in tasks}
        for f in as_completed(futures):
            results.append(f.result())

    ok_count = sum(1 for r in results if r["ok"])
    return jsonify({"results": results, "total": len(results), "ok_count": ok_count})


# ---------------------------------------------------------------------------
# Routes — agents management
# ---------------------------------------------------------------------------

def _read_config() -> dict:
    config_path = Path(CONFIG_PATH_STORE["path"])
    if not config_path.is_file():
        return {}
    return json.loads(config_path.read_text(encoding="utf-8"))


def _write_config(config: dict):
    config_path = Path(CONFIG_PATH_STORE["path"])
    backup = config_path.parent / f"openclaw.json.bak.{time.strftime('%Y%m%d%H%M%S')}"
    backup.write_text(config_path.read_text(encoding="utf-8"), encoding="utf-8")
    config_path.write_text(json.dumps(config, ensure_ascii=False, indent=2), encoding="utf-8")


@app.route("/api/agents", methods=["GET"])
def list_agents():
    config = _read_config()
    agents_list = config.get("agents", {}).get("list", [])
    primary_model = (
        config.get("agents", {}).get("defaults", {}).get("model", {}).get("primary", "")
    )
    result = []
    for a in agents_list:
        is_main = a.get("id") == "main"
        result.append({
            "id": a.get("id", ""),
            "name": a.get("name", a.get("id", "")),
            "model": primary_model if is_main else a.get("model", ""),
            "is_main": is_main,
        })
    return jsonify(result)


@app.route("/api/agents/<agent_id>/model", methods=["PUT"])
def update_agent_model(agent_id: str):
    data = request.json or {}
    new_model = (data.get("model") or "").strip()

    config = _read_config()
    agents_list = config.get("agents", {}).get("list", [])

    found = False
    for a in agents_list:
        if a.get("id") == agent_id:
            if agent_id == "main":
                config.setdefault("agents", {}).setdefault("defaults", {}).setdefault("model", {})
                config["agents"]["defaults"]["model"]["primary"] = new_model
            else:
                if new_model:
                    a["model"] = new_model
                else:
                    a.pop("model", None)
            found = True
            break

    if not found:
        return jsonify({"error": f"Agent '{agent_id}' 不存在"}), 404

    _write_config(config)
    return jsonify({"ok": True, "agent_id": agent_id, "model": new_model})


# ---------------------------------------------------------------------------
# Routes — config file
# ---------------------------------------------------------------------------

@app.route("/api/config/path", methods=["GET"])
def get_config_path():
    return jsonify({"path": CONFIG_PATH_STORE["path"]})


@app.route("/api/config/path", methods=["POST"])
def set_config_path():
    data = request.json or {}
    p = (data.get("path") or "").strip()
    if not p:
        return jsonify({"error": "路径不能为空"}), 400
    CONFIG_PATH_STORE["path"] = p
    # John Tang: 修改 - 同步持久化到数据库，重启后保留用户配置路径。20260319
    db = get_db()
    db.execute(
        "INSERT INTO settings (key, value) VALUES ('config_path', ?) "
        "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
        (p,),
    )
    db.commit()
    return jsonify({"ok": True, "path": p})


@app.route("/api/config", methods=["GET"])
def get_config():
    config_path = Path(CONFIG_PATH_STORE["path"])
    if not config_path.is_file():
        return jsonify({"error": f"文件不存在: {config_path}"}), 404
    try:
        config = json.loads(config_path.read_text(encoding="utf-8"))
        return jsonify(config)
    except Exception as e:
        return jsonify({"error": str(e)}), 500


@app.route("/api/config/apply", methods=["POST"])
def apply_config():
    data = request.json or {}
    primary = (data.get("primary") or "").strip()
    fallbacks = data.get("fallbacks", [])

    if not primary:
        return jsonify({"error": "必须选择主要模型 (primary)"}), 400

    try:
        result = apply_config_to_file(primary, fallbacks)
        return jsonify({"ok": True, **result})
    except Exception as e:
        return jsonify({"error": str(e)}), 500


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    init_db()
    app.run(host="0.0.0.0", port=6789, debug=True)
