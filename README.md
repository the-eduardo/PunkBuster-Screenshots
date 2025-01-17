# PunkBuster-Screenshots-to-Discord
Simple tool that sends punkbuster screenshots to a Discord channel using a Discord Bot app.

## Features

- Connects to an FTP or SFTP server based on configuration.
- Downloads screenshots and sends them to a Discord channel with Player Name and PB GUID.

## Prerequisites

Before running the program, make sure you have:

- Go installed (version 1.20+) (optional)
- Docker and Docker Compose installed
- A Discord Bot Token

## Usage

1. Clone this repository:

```bash
git clone https://github.com/pruu-networking/PunkBuster-Screenshots-to-Discord
cd PunkBuster-Screenshots-to-Discord
```

2. Configure the docker-compose.yml file:

```dotenv
SERVER=<FTP_or_SFTP_server>
USER=<Username>
PASS=<Password>
SFTP_FOLDER=<Server_folder_path>
BOT_TOKEN=<Discord_bot_token>
CHANNEL_ID=<Discord_channel_id>
SELECT_FTP_MODE=<sftp_or_ftp>
WAITING_TIME=<Waiting_time_in_minutes>
```

3. Then run it with docker-compose:

```bash
sudo docker-compose up --build -d
```
