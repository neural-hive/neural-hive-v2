import requests
from flask import Flask, request, jsonify

app = Flask(__name__)

# ============================================================
# CONFIG
# ============================================================

HOST = "127.0.0.1"
PORT = 10000

OLLAMA_URL = "http://127.0.0.1:11434"

# All models served through the same Python server.
MODELS = {
    "llama": "llama3.2:latest",
    "qwen": "qwen3:4b",
    "gemma": "gemma3:4b",
}

# Replace these with the actual Ethereum addresses of your
# corresponding Go agents if the Go health checker validates them.
AGENT_ADDRESSES = {
    "llama": "0x0000000000000000000000000000000000000001",
    "qwen":  "0x0000000000000000000000000000000000000002",
    "gemma": "0x0000000000000000000000000000000000000003",
}


# ============================================================
# REQUEST LOGGING
# ============================================================

@app.before_request
def log_request():

    print()
    print("=" * 80)
    print("INCOMING REQUEST")
    print("=" * 80)

    print("Method:", request.method)
    print("Path:", request.path)
    print("Content-Type:", request.content_type)
    print("Content-Length:", request.content_length)

    if request.method == "POST":

        print()
        print("Body:")
        print(request.get_data(as_text=True))

    print("=" * 80)


# ============================================================
# CHECK OLLAMA
# ============================================================

def get_ollama_models():

    try:

        response = requests.get(
            f"{OLLAMA_URL}/api/tags",
            timeout=5
        )

        response.raise_for_status()

        data = response.json()

        models = []

        for model in data.get("models", []):

            name = model.get("name")

            if name:
                models.append(name)

        return True, models

    except Exception as e:

        print()
        print("OLLAMA HEALTH ERROR:")
        print(e)

        return False, []


# ============================================================
# CHECK SPECIFIC MODEL
# ============================================================

def model_available(model_name):

    ollama_ok, installed_models = get_ollama_models()

    if not ollama_ok:
        return False

    return model_name in installed_models


# ============================================================
# GENERAL HEALTH
#
# GET /health
# ============================================================

@app.route("/health", methods=["GET"])
def health():

    ollama_ok, installed_models = get_ollama_models()

    if not ollama_ok:

        return jsonify({
            "ok": False,
            "agent": "ollama",
            "mode": "error"
        }), 503

    available = {}

    for name, model in MODELS.items():
        available[name] = model in installed_models

    # General server is healthy if at least one configured
    # model is available.
    any_available = any(available.values())

    if any_available:

        return jsonify({
            "ok": True,
            "agent": "ollama",
            "mode": "honest",
            "models": available
        }), 200

    return jsonify({
        "ok": False,
        "agent": "ollama",
        "mode": "error",
        "models": available
    }), 503


# ============================================================
# MODEL HEALTH
#
# GET /llama/health
# GET /qwen/health
# GET /gemma/health
# ============================================================

@app.route("/<model_name>/health", methods=["GET"])
def model_health(model_name):

    # --------------------------------------------------------
    # Validate model name
    # --------------------------------------------------------

    if model_name not in MODELS:

        return jsonify({
            "ok": False,
            "error": f"Unknown model '{model_name}'"
        }), 404

    model = MODELS[model_name]
    agent = AGENT_ADDRESSES[model_name]

    print()
    print("=" * 80)
    print("MODEL HEALTH")
    print("=" * 80)
    print("Route:", model_name)
    print("Model:", model)
    print("Agent:", agent)
    print("=" * 80)

    # --------------------------------------------------------
    # Check model
    # --------------------------------------------------------

    if model_available(model):

        response = {
            "ok": True,
            "agent": agent,
            "mode": "honest"
        }

        print("Health response:")
        print(response)

        return jsonify(response), 200

    response = {
        "ok": False,
        "agent": agent,
        "mode": "error"
    }

    print("Model unavailable")
    print(response)

    return jsonify(response), 503


# ============================================================
# TASK
#
# POST /llama/task
# POST /qwen/task
# POST /gemma/task
#
# ============================================================

@app.route("/<model_name>/task", methods=["POST"])
def model_task(model_name):

    # ========================================================
    # VALIDATE MODEL
    # ========================================================

    if model_name not in MODELS:

        return jsonify({
            "ok": False,
            "error": f"Unknown model '{model_name}'"
        }), 404

    model = MODELS[model_name]

    # ========================================================
    # PARSE JSON
    # ========================================================

    data = request.get_json(silent=True)

    if data is None:

        return jsonify({
            "ok": False,
            "error": "Invalid JSON body"
        }), 400

    print()
    print("=" * 80)
    print("TASK REQUEST")
    print("=" * 80)

    print("Agent:", model_name)
    print("Model:", model)

    print()
    print("Parsed request:")
    print(data)

    # ========================================================
    # PROMPT
    # ========================================================

    prompt = data.get("prompt")

    if not isinstance(prompt, str) or not prompt.strip():

        return jsonify({
            "ok": False,
            "error": "Missing or empty prompt"
        }), 400

    prompt = prompt.strip()

    task_id = data.get("taskId")
    sub_task_id = data.get("subTaskId")

    print()
    print("Task ID:", task_id)
    print("SubTask ID:", sub_task_id)
    print("Prompt:", prompt)

    print("=" * 80)

    # ========================================================
    # CALL OLLAMA
    # ========================================================

    try:

        print()
        print("CALLING OLLAMA")
        print("Model:", model)
        print("URL:", f"{OLLAMA_URL}/api/generate")

        ollama_response = requests.post(
            f"{OLLAMA_URL}/api/generate",
            json={
                "model": model,
                "prompt": prompt,
                "stream": False
            },
            timeout=600
        )

        print()
        print("Ollama HTTP status:")
        print(ollama_response.status_code)

        ollama_response.raise_for_status()

        result = ollama_response.json()

        print()
        print("Raw Ollama response:")
        print(result)

        # ====================================================
        # EXTRACT ANSWER
        # ====================================================

        answer = result.get("response", "")

        if not answer:

            print()
            print("ERROR: Model returned no answer")

            return jsonify({
                "ok": False,
                "error": "Model returned empty response",
                "model": model,
                "provider": "ollama"
            }), 502

        answer = answer.strip()

        print()
        print("Extracted answer:")
        print(repr(answer))

        # ====================================================
        # RESPONSE TO GO AGENT
        #
        # Compatible with your Answerer:
        #
        # Answer(...)
        #     -> text
        #     -> model
        #     -> provider
        #
        # ====================================================

        output = {
            "ok": True,
            "answer": answer,
            "response": answer,
            "model": model,
            "provider": "ollama",
            "taskId": task_id,
            "subTaskId": sub_task_id
        }

        print()
        print("=" * 80)
        print("RESPONSE TO GO AGENT:")
        print(output)
        print("=" * 80)

        return jsonify(output), 200

    # ========================================================
    # OLLAMA CONNECTION ERROR
    # ========================================================

    except requests.exceptions.ConnectionError as e:

        print()
        print("OLLAMA CONNECTION ERROR:")
        print(e)

        return jsonify({
            "ok": False,
            "error": "Ollama unavailable",
            "model": model,
            "provider": "ollama"
        }), 503

    # ========================================================
    # OLLAMA TIMEOUT
    # ========================================================

    except requests.exceptions.Timeout as e:

        print()
        print("OLLAMA TIMEOUT:")
        print(e)

        return jsonify({
            "ok": False,
            "error": "Ollama timeout",
            "model": model,
            "provider": "ollama"
        }), 504

    # ========================================================
    # OLLAMA HTTP ERROR
    # ========================================================

    except requests.exceptions.HTTPError as e:

        print()
        print("OLLAMA HTTP ERROR:")
        print(e)

        print()
        print("Ollama response:")
        print(ollama_response.text)

        return jsonify({
            "ok": False,
            "error": "Ollama HTTP error",
            "details": str(e),
            "model": model,
            "provider": "ollama"
        }), 502

    # ========================================================
    # OTHER ERROR
    # ========================================================

    except Exception as e:

        print()
        print("UNEXPECTED ERROR:")
        print(repr(e))

        return jsonify({
            "ok": False,
            "error": str(e)
        }), 500


# ============================================================
# ROOT
# ============================================================

@app.route("/", methods=["GET"])
def root():

    return jsonify({
        "ok": True,
        "service": "neural-hive-ollama-agents",
        "port": PORT,

        "models": MODELS,

        "endpoints": {
            "health": "GET /health",

            "llama_health": "GET /llama/health",
            "llama_task": "POST /llama/task",

            "qwen_health": "GET /qwen/health",
            "qwen_task": "POST /qwen/task",

            "gemma_health": "GET /gemma/health",
            "gemma_task": "POST /gemma/task"
        }
    }), 200


# ============================================================
# START SERVER
# ============================================================

if __name__ == "__main__":

    print()
    print("=" * 80)
    print("NEURAL HIVE OLLAMA AGENT SERVER")
    print("=" * 80)

    print()
    print("Server:")
    print(f"  http://{HOST}:{PORT}")

    print()
    print("Ollama:")
    print(f"  {OLLAMA_URL}")

    print()
    print("Models:")

    for name, model in MODELS.items():

        print(f"  {name:<10} -> {model}")

    print()
    print("Health endpoints:")

    print("  GET /llama/health")
    print("  GET /qwen/health")
    print("  GET /gemma/health")

    print()
    print("Task endpoints:")

    print("  POST /llama/task")
    print("  POST /qwen/task")
    print("  POST /gemma/task")

    print()
    print("=" * 80)
    print()

    app.run(
        host=HOST,
        port=PORT,
        debug=False,
        threaded=True
    )
