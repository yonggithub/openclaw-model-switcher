#!/usr/bin/env python3
# update_openclaw_models.py — 从 API 拉取模型列表并更新 openclaw 配置中的 models 与 agents.defaults

import argparse
import json
import sys
from pathlib import Path

try:
    import requests
except ImportError:
    print("错误：需要安装 requests。请运行: pip install requests")
    sys.exit(1)

DEFAULT_CONFIG_PATH = "/mnt/c/Code/openclawfilegenerate/openclaw.json"
MODELS_URL = "https://cliproxy.tgoo.top:8088/v1/models"
API_KEY = "6980bfe8-98dc-8320-9370-d873d2445139"

# 模型模板字段
MODEL_TEMPLATE = {
    "reasoning": False,
    "input": ["text"],
    "cost": {
        "input": 0,
        "output": 0,
        "cacheRead": 0,
        "cacheWrite": 0,
    },
    "contextWindow": 200000,
    "maxTokens": 8192,
}


def fetch_models() -> list[dict]:
    """请求 /v1/models，先 Bearer 再 x-api-key，解析为模型列表。"""
    for name, headers in [
        ("Authorization: Bearer", {"Authorization": f"Bearer {API_KEY}"}),
        ("x-api-key", {"x-api-key": API_KEY}),
    ]:
        try:
            r = requests.get(MODELS_URL, headers=headers, timeout=30)
            if r.status_code == 200:
                data = r.json()
                break
            # 非 200 则尝试下一种认证
            continue
        except requests.RequestException as e:
            print(f"请求失败 ({name}): {e}")
            continue
    else:
        raise RuntimeError(
            f"无法获取模型列表：两次认证均未返回 200，最后状态码: {r.status_code}"
        )

    # OpenAI 风格: { "data": [ { "id": "..." }, ... ] } 或直接 [ { "id": "..." }, ... ]
    if isinstance(data, list):
        raw_list = data
    elif isinstance(data, dict) and "data" in data:
        raw_list = data["data"]
    else:
        raise ValueError(f"无法解析响应结构，期望 list 或 {{'data': list}}，得到: {type(data)}")

    return raw_list


def build_models_list(raw_list: list) -> list[dict]:
    """用模板字段构建 models 数组，每项含 id、name 及模板。"""
    models = []
    for m in raw_list:
        if not isinstance(m, dict):
            continue
        mid = m.get("id") or m.get("model") or m.get("name")
        if not mid or not isinstance(mid, str):
            continue
        name = m.get("name") or m.get("id") or mid
        entry = {
            "id": mid,
            "name": name if isinstance(name, str) else mid,
            **MODEL_TEMPLATE,
        }
        models.append(entry)
    return models


def update_config(config: dict, models: list[dict]) -> None:
    """原地更新 config：models.providers.openai.models 与 agents.defaults。"""
    # 确保结构存在
    if "models" not in config:
        config["models"] = {}
    if "providers" not in config["models"]:
        config["models"]["providers"] = {}
    if "openai" not in config["models"]["providers"]:
        config["models"]["providers"]["openai"] = {}

    openai_provider = config["models"]["providers"]["openai"]
    # 只替换 models 键，保留其他 provider 字段
    openai_provider["models"] = models

    model_ids = [m["id"] for m in models]
    if not model_ids:
        return

    # agents.defaults
    if "agents" not in config:
        config["agents"] = {}
    if "defaults" not in config["agents"]:
        config["agents"]["defaults"] = {}

    defaults = config["agents"]["defaults"]
    if "models" not in defaults:
        defaults["models"] = {}
    # 每个 openai/MODEL_ID -> {}
    for mid in model_ids:
        key = f"openai/{mid}"
        if key not in defaults["models"]:
            defaults["models"][key] = {}

    # primary: 保留已有，否则用第一个
    if "model" not in defaults:
        defaults["model"] = {}
    model_cfg = defaults["model"]
    if "primary" not in model_cfg or not model_cfg["primary"]:
        model_cfg["primary"] = f"openai/{model_ids[0]}"


def main() -> None:
    parser = argparse.ArgumentParser(
        description="从 API 拉取模型并更新 openclaw 配置中的 models 与 agents.defaults"
    )
    parser.add_argument(
        "config_path",
        nargs="?",
        default=DEFAULT_CONFIG_PATH,
        help=f"配置文件 JSON 路径（默认: {DEFAULT_CONFIG_PATH}）",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="仅打印摘要（数量 + 前 3 个 id），不写文件",
    )
    args = parser.parse_args()

    config_path = Path(args.config_path)
    if not config_path.is_file():
        print(f"错误：配置文件不存在: {config_path}")
        sys.exit(1)

    print(f"读取配置: {config_path}")
    try:
        config = json.loads(config_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        print(f"错误：无法读取或解析 JSON: {e}")
        sys.exit(1)

    print("正在拉取模型列表...")
    try:
        raw_list = fetch_models()
    except (ValueError, RuntimeError) as e:
        print(f"错误：{e}")
        sys.exit(1)

    models = build_models_list(raw_list)
    if not models:
        print("警告：未解析到任何模型，将不会更新 models 与 agents.defaults。")

    print(f"解析到 {len(models)} 个模型。")
    if models:
        first_3 = [m["id"] for m in models[:3]]
        print(f"前 3 个 id: {first_3}")

    if args.dry_run:
        print("--dry-run：不写入文件。")
        return

    update_config(config, models)

    try:
        config_path.write_text(
            json.dumps(config, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    except OSError as e:
        print(f"错误：无法写入文件: {e}")
        sys.exit(1)

    print(f"已更新并保存: {config_path}")


if __name__ == "__main__":
    main()
