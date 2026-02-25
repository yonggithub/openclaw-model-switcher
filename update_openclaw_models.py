#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
从远程 API 拉取模型列表，按 provider (owned_by) 更新 config.models.providers，
并完整替换 agents.defaults.models 与 model.primary。
"""

import argparse
import json
import sys
import time
from pathlib import Path

try:
    import requests
except ImportError:
    print("错误：需要安装 requests。请运行: pip install requests")
    sys.exit(1)

DEFAULT_CONFIG_PATH = "/mnt/c/Code/openclawfilegenerate/openclaw.json"
MODELS_URL = "https://cliproxy.tgoo.top:8088/v1/models"
API_KEY = "6980bfe8-98dc-8320-9370-d873d2445139"

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


def fetch_models() -> list:
    """请求 /v1/models：先 Bearer，非 200 则用 x-api-key，解析为原始模型列表。"""
    data = None
    last_status = None

    for auth_name, headers in [
        ("Authorization: Bearer", {"Authorization": f"Bearer {API_KEY}"}),
        ("x-api-key", {"x-api-key": API_KEY}),
    ]:
        try:
            r = requests.get(MODELS_URL, headers=headers, timeout=30)
            last_status = r.status_code
            if r.status_code == 200:
                data = r.json()
                break
            print(f"  {auth_name} 返回 {r.status_code}，尝试下一种认证…")
        except requests.RequestException as e:
            print(f"  请求失败 ({auth_name}): {e}")
            continue

    if data is None:
        raise RuntimeError(
            f"无法获取模型列表：两种认证均未返回 200，最后状态码: {last_status}"
        )

    if isinstance(data, list):
        raw_list = data
    elif isinstance(data, dict) and "data" in data:
        raw_list = data["data"]
    else:
        raise ValueError(
            f"无法解析响应结构，期望 list 或 {{'data': list}}，得到: {type(data)}"
        )
    return raw_list


def build_per_provider_models(raw_list: list) -> tuple[dict[str, list], list[tuple[str, str]]]:
    """
    按 owned_by 分 provider，为每个 provider 构建模型列表（模板字段，name=id）。
    返回 (provider -> models_list, 保持 API 顺序的 (provider, id) 列表)。
    """
    provider_models: dict[str, list] = {}
    ordered_keys: list[tuple[str, str]] = []  # (provider, id) 保持 API 顺序

    for m in raw_list:
        if not isinstance(m, dict):
            continue
        mid = m.get("id") or m.get("model") or m.get("name")
        if not mid or not isinstance(mid, str):
            continue
        provider = (m.get("owned_by") or "").strip() or "openai"
        if not isinstance(provider, str):
            provider = "openai"

        entry = {
            "id": mid,
            "name": mid,
            **MODEL_TEMPLATE,
        }
        if provider not in provider_models:
            provider_models[provider] = []
        provider_models[provider].append(entry)
        ordered_keys.append((provider, mid))

    return provider_models, ordered_keys


def update_config(
    config: dict,
    provider_models: dict[str, list],
    ordered_keys: list[tuple[str, str]],
) -> None:
    """
    原地更新 config：
    - 对 API 返回的每个 provider：创建/更新 config.models.providers[provider]，
      从 openai 复制 baseUrl/apiKey/api/mode（若存在），再设置 models 列表；其他 provider 不动。
    - agents.defaults.models 替换为恰好所有 provider/id 键（值为 {}）。
    - agents.defaults.model.primary：若仍在新的 key 集合中则保留，否则设为 API 顺序的第一个 provider/id。
    """
    if "models" not in config:
        config["models"] = {}
    if "providers" not in config["models"]:
        config["models"]["providers"] = {}

    providers = config["models"]["providers"]
    openai_ref = providers.get("openai") or {}

    # 从 openai 拷贝的字段（若存在）
    copy_keys = ("baseUrl", "apiKey", "api", "mode")

    for provider, models_list in provider_models.items():
        if provider not in providers:
            providers[provider] = {}
        dest = providers[provider]
        for k in copy_keys:
            if k in openai_ref and k not in dest:
                dest[k] = openai_ref[k]
        dest["models"] = models_list

    # 构建完整的 model key 集合（与 agents.defaults.models 一致）
    new_models_map = {f"{p}/{i}": {} for p, i in ordered_keys}
    if not new_models_map:
        return

    if "agents" not in config:
        config["agents"] = {}
    if "defaults" not in config["agents"]:
        config["agents"]["defaults"] = {}

    defaults = config["agents"]["defaults"]
    defaults["models"] = new_models_map

    if "model" not in defaults:
        defaults["model"] = {}
    model_cfg = defaults["model"]
    current_primary = model_cfg.get("primary")
    if current_primary and current_primary in new_models_map:
        # 保留原有 primary
        pass
    else:
        first_key = f"{ordered_keys[0][0]}/{ordered_keys[0][1]}"
        model_cfg["primary"] = first_key


def main() -> None:
    parser = argparse.ArgumentParser(
        description="从 API 拉取模型并按 provider 更新 openclaw 配置中的 models 与 agents.defaults"
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
        help="仅打印摘要（数量、前 3 个 id、前 3 个 provider/id），不写文件",
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

    print("正在拉取模型列表…")
    try:
        raw_list = fetch_models()
    except (ValueError, RuntimeError) as e:
        print(f"错误：{e}")
        sys.exit(1)

    try:
        provider_models, ordered_keys = build_per_provider_models(raw_list)
    except Exception as e:
        print(f"错误：构建模型列表时出错: {e}")
        sys.exit(1)

    total = sum(len(v) for v in provider_models.values())
    if total == 0:
        print("警告：未解析到任何模型，将不会更新 models 与 agents.defaults。")
        if args.dry_run:
            print("--dry-run：未写入文件。")
        sys.exit(0)

    first_3_ids = [ordered_keys[i][1] for i in range(min(3, len(ordered_keys)))]
    first_3_pairs = [f"{p}/{i}" for p, i in ordered_keys[:3]]

    print(f"解析到 {total} 个模型，来自 {len(provider_models)} 个 provider。")
    print(f"前 3 个 id: {first_3_ids}")
    print(f"前 3 个 provider/id: {first_3_pairs}")

    if args.dry_run:
        print("--dry-run：不写入文件。")
        return

    update_config(config, provider_models, ordered_keys)

    # 写入前备份当前配置文件，失败则中止
    backup_path = config_path.parent / f"openclaw.json.bak.{time.strftime('%Y%m%d%H%M%S')}"
    try:
        backup_path.write_text(config_path.read_text(encoding="utf-8"), encoding="utf-8")
    except OSError as e:
        print(f"错误：无法创建备份 {backup_path}: {e}")
        sys.exit(1)

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
