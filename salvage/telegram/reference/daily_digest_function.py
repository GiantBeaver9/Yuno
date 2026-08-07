"""Azure Functions timer trigger that sends a daily digest to your Telegram chat.

Drop this into your Functions app (Python v2 programming model). It reuses the
same bot token as bot.py — sending never conflicts with the PC bot's polling.

App settings required (Function App > Environment variables, or Key Vault refs):
    TELEGRAM_BOT_TOKEN   same token as the PC bot
    TELEGRAM_CHAT_ID     your numeric Telegram user ID
"""

import logging
import os

import azure.functions as func
import requests

app = func.FunctionApp()

TELEGRAM_MESSAGE_LIMIT = 4096


def send_telegram(text: str) -> None:
    token = os.environ["TELEGRAM_BOT_TOKEN"]
    chat_id = os.environ["TELEGRAM_CHAT_ID"]
    url = f"https://api.telegram.org/bot{token}/sendMessage"
    # Telegram rejects messages over 4096 chars, counted in UTF-16 code units
    # (emoji count as 2) — send in chunks measured the same way.
    while text:
        chunk = text[:TELEGRAM_MESSAGE_LIMIT]
        while len(chunk.encode("utf-16-le")) // 2 > TELEGRAM_MESSAGE_LIMIT:
            chunk = chunk[:-1]
        text = text[len(chunk):]
        resp = requests.post(url, json={"chat_id": chat_id, "text": chunk}, timeout=30)
        resp.raise_for_status()


def build_digest() -> str:
    """Replace this with your actual digest assembly (or call your existing
    orchestration's output — e.g. read the blob/queue message it produced)."""
    return "☀️ Your daily digest\n\n- item one\n- item two"


# 07:00 UTC daily; adjust to your timezone (CRON is UTC unless you set
# WEBSITE_TIME_ZONE on the Function App).
@app.timer_trigger(schedule="0 0 7 * * *", arg_name="timer", run_on_startup=False)
def daily_digest(timer: func.TimerRequest) -> None:
    digest = build_digest()
    send_telegram(digest)
    logging.info("Daily digest sent to Telegram (%d chars)", len(digest))
