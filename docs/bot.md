# Set up a Telegram bot

## Create it

1. Open the official [@BotFather](https://t.me/BotFather).
2. Send `/newbot` and follow the prompts.
3. Keep the token private. Anyone with it can control the bot.
4. Open your new bot and press **Start**.

Telegram's [bot tutorial](https://core.telegram.org/bots/tutorial) covers the
same flow in more detail.

Put the token in `telegram.bot_token` and protect the config with `chmod 0600`.
You can also provide a nonempty `GONG_BOT_TOKEN`.

## Use a private chat

Press **Start** or send `/start` to the bot, then [find the chat ID](chat-id.md).
Bots receive messages from private chats regardless of group privacy settings
([Telegram Bot FAQ](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get)).

## Use a group

Add the bot to the group and send `/start@BOT_USERNAME` there. Replace
`BOT_USERNAME` with your bot's username. This addressed command works with
Telegram's default privacy mode, so you do not need to disable it
([privacy mode](https://core.telegram.org/bots/features#privacy-mode)).

For a regular `mode: chat` target, let the bot send messages. Forum creation,
pinning, and deletion need the rights listed in [Forum topics](topics.md#permissions).
For topics, follow the [group setup steps](topics.md#set-up-the-group-first)
before copying the chat ID: enabling topics can convert the group to a
supergroup with a new ID.

Once `message.chat.id` is in your config, send one real notification. `/health`
only checks that Gong is running; it does not test Telegram or bot permissions.
