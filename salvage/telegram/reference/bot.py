"""Personal Telegram bot bridged to a local LLM.

Only responds to the Telegram user IDs listed in ALLOWED_USER_IDS.
Talks to any OpenAI-compatible chat completions endpoint
(Ollama, LM Studio, llama.cpp server, vLLM, ...).
"""

import logging
import os

import httpx
from dotenv import load_dotenv
from telegram import Update
from telegram.constants import ChatAction
from telegram.ext import (
    Application,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)

load_dotenv()

TELEGRAM_TOKEN = os.environ["TELEGRAM_BOT_TOKEN"]
ALLOWED_USER_IDS = {
    int(uid) for uid in os.environ["ALLOWED_USER_IDS"].replace(",", " ").split()
}

LLM_BASE_URL = os.getenv("LLM_BASE_URL", "http://localhost:1234/v1")
# Leave empty to auto-detect the model currently loaded in LM Studio.
LLM_MODEL = os.getenv("LLM_MODEL", "")
LLM_API_KEY = os.getenv("LLM_API_KEY", "not-needed")  # local servers usually ignore it
SYSTEM_PROMPT = os.getenv("SYSTEM_PROMPT", "You are a helpful assistant.")
MAX_HISTORY_MESSAGES = int(os.getenv("MAX_HISTORY_MESSAGES", "40"))
LLM_TIMEOUT_SECONDS = float(os.getenv("LLM_TIMEOUT_SECONDS", "300"))

TELEGRAM_MESSAGE_LIMIT = 4096

logging.basicConfig(
    format="%(asctime)s %(levelname)s %(name)s: %(message)s", level=logging.INFO
)
# The HTTP libraries log every polling request at INFO; quiet them down.
logging.getLogger("httpx").setLevel(logging.WARNING)
log = logging.getLogger("bot")

# chat_id -> list of {"role": ..., "content": ...} (system prompt not stored)
histories: dict[int, list[dict[str, str]]] = {}


def is_allowed(update: Update) -> bool:
    user = update.effective_user
    chat = update.effective_chat
    # Only the allowlisted user, and only in a one-on-one chat — the bot stays
    # silent in groups/channels even if someone manages to add it to one.
    allowed = (
        user is not None
        and user.id in ALLOWED_USER_IDS
        and chat is not None
        and chat.type == chat.PRIVATE
    )
    if not allowed and user is not None:
        log.warning("Ignoring message from user %s (%s) in %s chat",
                    user.id, user.username, chat.type if chat else "?")
    return allowed


async def resolve_model(client: httpx.AsyncClient) -> str:
    """Return the configured model, or the one currently loaded in LM Studio."""
    global LLM_MODEL
    if LLM_MODEL:
        return LLM_MODEL
    resp = await client.get(f"{LLM_BASE_URL.rstrip('/')}/models")
    resp.raise_for_status()
    models = resp.json().get("data", [])
    if not models:
        raise RuntimeError(
            "No model loaded in the LLM server — load one in LM Studio first."
        )
    LLM_MODEL = models[0]["id"]
    log.info("Auto-detected model: %s", LLM_MODEL)
    return LLM_MODEL


async def query_llm(messages: list[dict[str, str]]) -> str:
    headers = {"Authorization": f"Bearer {LLM_API_KEY}"}
    async with httpx.AsyncClient(
        timeout=LLM_TIMEOUT_SECONDS, headers=headers
    ) as client:
        payload = {
            "model": await resolve_model(client),
            "messages": [{"role": "system", "content": SYSTEM_PROMPT}, *messages],
            "stream": False,
        }
        resp = await client.post(
            f"{LLM_BASE_URL.rstrip('/')}/chat/completions",
            json=payload,
        )
        resp.raise_for_status()
        data = resp.json()
    return data["choices"][0]["message"]["content"]


def split_message(text: str, limit: int = TELEGRAM_MESSAGE_LIMIT) -> list[str]:
    """Split text into Telegram-sized chunks, preferring newline boundaries.

    Telegram counts the limit in UTF-16 code units, not codepoints — emoji
    count as 2 — so we measure in UTF-16 too.
    """
    chunks = []
    while len(text.encode("utf-16-le")) // 2 > limit:
        window = text[:limit]  # UTF-16 length >= len(), so this can't be short
        while len(window.encode("utf-16-le")) // 2 > limit:
            window = window[:-1]
        cut = window.rfind("\n")
        if cut <= 0:
            cut = len(window)
        chunks.append(text[:cut])
        text = text[cut:].lstrip("\n")
    if text:
        chunks.append(text)
    return chunks


async def cmd_start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_allowed(update):
        return
    model = LLM_MODEL or "auto-detected from LM Studio"
    await update.message.reply_text(
        f"Hi! I'm your local LLM ({model}). Send me a message.\n"
        "Commands: /reset — clear conversation history"
    )


async def cmd_reset(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_allowed(update):
        return
    histories.pop(update.effective_chat.id, None)
    await update.message.reply_text("Conversation history cleared.")


async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_allowed(update):
        return

    chat_id = update.effective_chat.id
    history = histories.setdefault(chat_id, [])
    history.append({"role": "user", "content": update.message.text})

    await context.bot.send_chat_action(chat_id=chat_id, action=ChatAction.TYPING)
    try:
        reply = await query_llm(history)
    except httpx.ConnectError:
        history.pop()
        await update.message.reply_text(
            f"Can't reach the LLM server at {LLM_BASE_URL}. Is it running?"
        )
        return
    except Exception:
        history.pop()
        log.exception("LLM request failed")
        await update.message.reply_text("LLM request failed — check the bot logs.")
        return

    history.append({"role": "assistant", "content": reply})
    # Trim in pairs so history always starts with a user message.
    if len(history) > MAX_HISTORY_MESSAGES:
        del history[: len(history) - MAX_HISTORY_MESSAGES]
        if history and history[0]["role"] == "assistant":
            del history[0]

    for chunk in split_message(reply):
        await update.message.reply_text(chunk)


def main() -> None:
    app = Application.builder().token(TELEGRAM_TOKEN).build()
    private = filters.ChatType.PRIVATE
    app.add_handler(CommandHandler("start", cmd_start, filters=private))
    app.add_handler(CommandHandler("reset", cmd_reset, filters=private))
    app.add_handler(
        MessageHandler(private & filters.TEXT & ~filters.COMMAND, handle_message)
    )

    log.info("Bot starting. Allowed users: %s. LLM: %s at %s",
             sorted(ALLOWED_USER_IDS), LLM_MODEL or "(auto)", LLM_BASE_URL)
    app.run_polling(allowed_updates=Update.ALL_TYPES)


if __name__ == "__main__":
    main()
