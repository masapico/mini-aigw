# 閉域・オフライン環境向けデプロイ・運用手順書 (Linux / Windows)

本ドキュメントは、インターネット接続のない**完全閉域環境（オフライン・エアギャップ環境）**の本番運用サーバー（AWS EC2 Linux / Windows）に対し、`mini-aigw` の単一バイナリおよび関連設定を安全かつ確実にデプロイ・運用するための完全マニュアルです。

---

## 1. 概要とデプロイアーキテクチャ

### 1.1 単一バイナリによる閉域運用のメリット
`mini-aigw` は Go 言語（CGO フリー / 純Go SQLite）で実装されており、Web UI の HTML テンプレートや静的アセット（Bootstrap 5.3 + htmx 2.x）が実行バイナリ内部に完全内包（Go `embed.FS`）されています。

### 1.2 持ち込み対象（配布物アーカイブ）の構成
オンライン作業端末でクロスコンパイルリリーススクリプト（`scripts/release`）を実行して生成されるアーカイブ（zip / tar.gz）を持ち込みます。

```
offline_deploy_bundle/
├── mini-aigw                     # 1. 実行バイナリ (Linux: mini-aigw / Windows: mini-aigw.exe)
├── .env.sample                   # 2. 環境変数設定サンプル
├── README.md                     # 3. 運用・APIリファレンス
└── tools/                        # 4. オプションツール（任意）
    └── xberg                     #    xberg CLI バイナリ (PDF/Office等高度抽出用)
```

### 1.3 想定インフラ環境（AWS EC2 / 完全閉域 VPC）
本アプリケーションは、セキュリティ要件の厳しい企業・組織向けに、**AWS EC2 インスタンス（Amazon Linux 2023, RHEL 9, Ubuntu 22.04/24.04, Windows Server 等）** 上での本番稼働を主たるターゲットとして設計・検証されています。

特に、インターネットゲートウェイ (IGW) や NAT ゲートウェイを一切持たない**完全閉域（エアギャップ）プライベートサブネット内の EC2** での運用を想定しています。

---

## 2. 完全閉域環境における VPC エンドポイント設計

完全閉域 VPC では、パブリックインターネットへのアウトバウンド通信が存在しないため、`mini-aigw` が AWS サービス（Bedrock、S3 等）と通信するためには **VPC エンドポイント（AWS PrivateLink および Gateway 型エンドポイント）** の作成が必須となります。

以下のエンドポイントを対象 VPC に配置してください：

| サービス種別 | VPC エンドポイント サービス名 | タイプ | 用途 / 備考 |
|---|---|---|---|
| **Bedrock Runtime** | `com.amazonaws.<region>.bedrock-runtime` | Interface | Converse API / ConverseStream API による LLM 推論 |
| **Bedrock Agent Runtime** | `com.amazonaws.<region>.bedrock-agent-runtime` | Interface | Bedrock Knowledge Base (S3 Vectors / Managed Store) の検索 (`Retrieve`) |
| **Bedrock Agent** | `com.amazonaws.<region>.bedrock-agent` | Interface | ナレッジベース同期ジョブ (`StartIngestionJob`, `ListIngestionJobs`) |
| **Amazon S3** | `com.amazonaws.<region>.s3` | Gateway (推奨) / Interface | ナレッジ原本ファイルおよびサイドカーメタデータ (`.metadata.json`) のアップロード・取得・削除。<br>※ 同一リージョン内の VPC であれば Gateway 型（無料・ルートテーブル設定）が推奨です。 |
| *(運用用)* **AWS Systems Manager (SSM)** | `com.amazonaws.<region>.ssm`<br>`com.amazonaws.<region>.ssmmessages`<br>`com.amazonaws.<region>.ec2messages` | Interface | 閉域 EC2 に対し、SSH ポートを開放せずマネジメントコンソールや CLI からセッションマネージャー経由で安全に接続・運用する場合に必要 |
| *(認証用)* **社内 OIDC IdP** | mini-id / Keycloak 等 | - | 認証に mini-id (OIDC) を使用する場合、社内閉域ネットワーク（Direct Connect / Transit Gateway / VPN）または VPC エンドポイントサービス経由でアクセス可能な経路を確保 |

> **VPC エンドポイント設定の重要ポイント**:
> 1. **プライベート DNS の有効化 (Private DNS Name)**:
>    - Interface 型エンドポイント作成時、「プライベート DNS 名を有効化 (Enable Private DNS Name)」に必ずチェックを入れてください。
>    - これにより、AWS SDK for Go v2 が内部で発行する標準のエンドポイント URL（例: `bedrock-runtime.ap-northeast-1.amazonaws.com`）が、自動的に VPC エンドポイントのプライベート ENI IP アドレスへ名前解決されます。
> 2. **セキュリティグループ設定**:
>    - Interface 型 VPC エンドポイントにアタッチするセキュリティグループのインバウンドルールで、**EC2 インスタンスからの HTTPS (443/TCP) 通信を許可** してください。
> 3. **IAM ロール（インスタンスプロファイル）**:
>    - EC2 に **IAM ロール（インスタンスプロファイル）** をアタッチすることで、AWS SDK for Go v2 がインスタンスメタデータサービス (IMDSv2) から一時クレデンシャルを自動的かつ安全に取得します。APIキー等の静的認証情報を `.env` に記述する必要はありません。

---

## 3. 配布パッケージの作成（オンライン環境）

インターネットに接続可能な作業端末（Linux / macOS / Windows）で、クロスコンパイルスクリプトを実行して配布パッケージを生成します。

```bash
# 全プラットフォーム向けリリースビルド（dist/ に zip / tar.gz が生成されます）
go run ./scripts/release -version v1.0.0

# 対象 OS を Linux に絞る場合:
go run ./scripts/release -version v1.0.0 -os linux -arch amd64

# 対象 OS を Windows に絞る場合:
go run ./scripts/release -version v1.0.0 -os windows -arch amd64
```

生成されたアーカイブ（例: `mini-aigw_v1.0.0_linux_amd64.tar.gz` や `mini-aigw_v1.0.0_windows_amd64.zip`）およびチェックサムファイル（`SHA256SUMS.txt`）を、USB メディアや踏み台経由で閉域環境サーバーへ転送します。

---

## 4. Linux 環境へのデプロイ・サービス常駐化手順

### 4.1 アーカイブの展開と配置

```bash
# デプロイ先ディレクトリの作成
sudo mkdir -p /opt/mini-aigw
cd /opt/mini-aigw

# アーカイブの展開
sudo tar -zxvf /path/to/mini-aigw_v1.0.0_linux_amd64.tar.gz

# 実行権限の付与
sudo chmod +x mini-aigw
```

### 4.2 設定ファイル (`.env`) の作成

```bash
sudo cp .env.sample .env
sudo vim .env
```

`.env` 内の本番固有パラメータを編集します：
```env
APP_NAME=mini-aigw
DEBUG=false
HOST=0.0.0.0
PORT=8000
SECRET_KEY=generate-a-strong-random-key-for-session

DATABASE_URL=sqlite:///data/aigw.db

AWS_REGION=ap-northeast-1
BEDROCK_KB_ID=your-bedrock-knowledge-base-id
BEDROCK_DATA_SOURCE_ID=your-bedrock-data-source-id
KNOWLEDGE_S3_BUCKET=your-s3-knowledge-bucket
DEFAULT_MODEL_ID=openai.gpt-oss-20b-1:0 # 既定モデルID (取得方法は GET_FOUNDATION_MODELS.md 参照)

# ドキュメント抽出 CLI パス (任意: xberg を配置した場合)
# XBERG_PATH=/usr/local/bin/xberg
```

### 4.3 専用実行ユーザーの作成とパーミッション

```bash
sudo useradd -r -s /sbin/nologin mini-aigw
sudo mkdir -p /opt/mini-aigw/data
sudo chown -R mini-aigw:mini-aigw /opt/mini-aigw
```

### 4.4 systemd サービス定義の作成

`/etc/systemd/system/mini-aigw.service` を作成します：

```ini
[Unit]
Description=mini-aigw AI Gateway Service
After=network.target

[Service]
Type=simple
User=mini-aigw
Group=mini-aigw
WorkingDirectory=/opt/mini-aigw
ExecStart=/opt/mini-aigw/mini-aigw
Restart=always
RestartSec=5
LimitNOFILE=65536
EnvironmentFile=/opt/mini-aigw/.env

[Install]
WantedBy=multi-user.target
```

### 4.5 サービスの起動と自動起動有効化

```bash
sudo systemctl daemon-reload
sudo systemctl enable mini-aigw
sudo systemctl start mini-aigw

# 稼働ステータス確認
sudo systemctl status mini-aigw

# ログ確認
journalctl -u mini-aigw -f
```

---

## 5. Windows Server 環境へのデプロイ・サービス常駐化手順

### 5.1 アーカイブの展開と配置

1. `C:\tools\mini-aigw` ディレクトリを作成します。
2. `mini-aigw_v1.0.0_windows_amd64.zip` を上記ディレクトリに展開します。
3. `C:\tools\mini-aigw\.env.sample` を `.env` にコピーし、環境変数を本番値に設定します。

### 5.2 NSSM による Windows サービス化

Windows 環境では、[NSSM (Non-Sucking Service Manager)](https://nssm.cc/) を用いてサービスとして登録・自動起動を設定します。

```powershell
# サービス登録
nssm install mini-aigw "C:\tools\mini-aigw\mini-aigw.exe"
nssm set mini-aigw AppDirectory "C:\tools\mini-aigw"
nssm set mini-aigw Start SERVICE_AUTO_START
nssm set mini-aigw AppStdout "C:\tools\mini-aigw\logs\stdout.log"
nssm set mini-aigw AppStderr "C:\tools\mini-aigw\logs\stderr.log"

# サービス起動
nssm start mini-aigw

# 状態確認
nssm status mini-aigw
```

---

## 6. 稼働確認

デプロイ完了後、以下の導通を確認します：

1. **管理画面アクセス**:
   - ブラウザで `http://<server-ip>:8000/login` へアクセスし、初期管理者アカウント（`admin` / `Password1234`）でログインできることを確認。
   - ブラウザの開発者ツールで外部 CDN（`cdn.jsdelivr.net` 等）への通信が一切発生せず、内包アセット（Bootstrap 5.3 + htmx 2.x）がローカルから 200 OK でロードされていることを確認。
2. **Bedrock / S3 導通**:
   - ナレッジ管理画面（`/knowledge`）でドキュメントのアップロードと Bedrock KB 同期ジョブの開始を確認。
   - RAG テスト画面（`/rag-test`）で検索クエリの Retrieve が正常に結果を返すことを確認。
3. **LLM 基盤モデルの登録と有効化**:
   - Web 管理画面の「モデル管理 (`/models`)」にて、運用で利用する Bedrock モデル ID / 推論プロファイル ID（例: `apac.anthropic.claude-3-5-sonnet-20241022-v2:0` 等）を登録・有効化・既定設定。
   - 登録可能なモデル情報の確認方法や Model access の前提条件については、[Bedrock モデル情報取得・登録ガイド (GET_FOUNDATION_MODELS.md)](GET_FOUNDATION_MODELS.md) を参照。
4. **OpenAI 互換 API 呼び出し**:
   - `curl` コマンドで `/v1/chat/completions` にリクエストを送信し、登録したモデルでの正常な推論応答が得られることを確認。

---

## 7. 関連ドキュメント

- [README.md - mini-aigw 全体仕様・起動手順](../README.md)
- [docs/GET_FOUNDATION_MODELS.md - Bedrock モデル情報取得・登録ガイド](GET_FOUNDATION_MODELS.md)
- [docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md - Bedrock Managed Knowledge Base 検索仕様・トラブルシューティング](BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md)

