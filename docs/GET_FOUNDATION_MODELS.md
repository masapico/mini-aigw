
AWSマネジメントコンソールの CloudShell で以下を実行すると、利用可能なFoundationモデルの一覧を取得できます。

下記の例では、ap-northeast-1 で利用可能なモデルを取得しています。

mini-aigwでは、下記の例の profileId をモデルとして登録し利用します。

```
aws bedrock list-inference-profiles --region ap-northeast-1 \
  --query "inferenceProfileSummaries[?status=='ACTIVE'].{
    profileId:inferenceProfileId,
    profileName:inferenceProfileName,
    type:type
  }" \
  --output table
  ```

  出力例: 

    ---------------------------------------------------------------------------------------------------------------
    ---------------------------------------------------------------------------------------------------------------
    |                                            ListInferenceProfiles                                            |
    +---------------------------------------------------+---------------------------------------+-----------------+
    |                     profileId                     |              profileName              |      type       |
    +---------------------------------------------------+---------------------------------------+-----------------+
    |  apac.anthropic.claude-3-sonnet-20240229-v1:0     |  APAC Anthropic Claude 3 Sonnet       |  SYSTEM_DEFINED |
    |  apac.anthropic.claude-3-5-sonnet-20240620-v1:0   |  APAC Anthropic Claude 3.5 Sonnet     |  SYSTEM_DEFINED |
    |  apac.anthropic.claude-3-haiku-20240307-v1:0      |  APAC Anthropic Claude 3 Haiku        |  SYSTEM_DEFINED |
    |  apac.anthropic.claude-3-5-sonnet-20241022-v2:0   |  APAC Anthropic Claude 3.5 Sonnet v2  |  SYSTEM_DEFINED |
    |  apac.amazon.nova-micro-v1:0                      |  APAC Nova Micro                      |  SYSTEM_DEFINED |
    |  apac.amazon.nova-lite-v1:0                       |  APAC Nova Lite                       |  SYSTEM_DEFINED |
    |  apac.amazon.nova-pro-v1:0                        |  APAC Nova Pro                        |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-sonnet-4-5-20250929-v1:0     |  JP Anthropic Claude Sonnet 4.5       |  SYSTEM_DEFINED |
    |  global.cohere.embed-v4:0                         |  Global Cohere Embed v4               |  SYSTEM_DEFINED |
    |  apac.anthropic.claude-sonnet-4-20250514-v1:0     |  APAC Claude Sonnet 4                 |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-haiku-4-5-20251001-v1:0      |  JP Anthropic Claude Haiku 4.5        |  SYSTEM_DEFINED |
    |  global.twelvelabs.pegasus-1-2-v1:0               |  GLOBAL TwelveLabs Pegasus v1.2       |  SYSTEM_DEFINED |
    |  global.amazon.nova-2-lite-v1:0                   |  GLOBAL Amazon Nova 2 Lite            |  SYSTEM_DEFINED |
    |  jp.amazon.nova-2-lite-v1:0                       |  JP Amazon Nova 2 Lite                |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-4-5-20251101-v1:0   |  GLOBAL Anthropic Claude Opus 4.5     |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-sonnet-4-6                   |  JP Anthropic Claude Sonnet 4.6       |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-opus-4-7                     |  JP Anthropic Claude Opus 4.7         |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-4-6-v1              |  Global Anthropic Claude Opus 4.6     |  SYSTEM_DEFINED |
    |  global.anthropic.claude-sonnet-4-5-20250929-v1:0 |  Global Claude Sonnet 4.5             |  SYSTEM_DEFINED |
    |  global.anthropic.claude-sonnet-4-20250514-v1:0   |  Global Claude Sonnet 4               |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-opus-4-8                     |  JP Anthropic Claude Opus 4.8         |  SYSTEM_DEFINED |
    |  global.anthropic.claude-haiku-4-5-20251001-v1:0  |  Global Anthropic Claude Haiku 4.5    |  SYSTEM_DEFINED |
    |  global.anthropic.claude-fable-5                  |  Global Anthropic Claude Fable 5      |  SYSTEM_DEFINED |
    |  global.openai.gpt-5.6-sol                        |  Global OpenAI GPT-5.6 Sol            |  SYSTEM_DEFINED |
    |  global.openai.gpt-5.6-terra                      |  Global OpenAI GPT-5.6 Terra          |  SYSTEM_DEFINED |
    |  global.xai.grok-4.6                              |  Global xAI Grok 4.6                  |  SYSTEM_DEFINED |
    |  global.anthropic.claude-fable-5-1                |  Global Anthropic Claude Fable 5.1    |  SYSTEM_DEFINED |
    |  global.twelvelabs.pegasus-1-5-v1:0               |  Global TwelveLabs Pegasus v1.5       |  SYSTEM_DEFINED |
    |  global.openai.gpt-6-sol                          |  Global OpenAI GPT-6 Sol              |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-opus-5-5                     |  JP Anthropic Claude Opus 5.5         |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-5-5                 |  Global Anthropic Claude Opus 5.5     |  SYSTEM_DEFINED |
    |  global.openai.gpt-5.4                            |  Global OpenAI GPT 5.4                |  SYSTEM_DEFINED |
    |  global.openai.gpt-5.5                            |  Global OpenAI GPT 5.5                |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-5                   |  Global Anthropic Claude Opus 5       |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-4-8                 |  Global Anthropic Claude Opus 4.8     |  SYSTEM_DEFINED |
    |  global.anthropic.claude-sonnet-5                 |  Global Anthropic Claude Sonnet 5     |  SYSTEM_DEFINED |
    |  global.anthropic.claude-opus-4-7                 |  Global Anthropic Claude Opus 4.7     |  SYSTEM_DEFINED |
    |  global.xai.grok-4.7                              |  Global xAI Grok 4.7                  |  SYSTEM_DEFINED |
    |  global.openai.gpt-6-astra                        |  Global OpenAI GPT-6 Astra            |  SYSTEM_DEFINED |
    |  global.openai.gpt-6.1-sol                        |  Global OpenAI GPT-6.1 Sol            |  SYSTEM_DEFINED |
    |  global.zai.glm-5.3                               |  Global Z.AI GLM 5.3                  |  SYSTEM_DEFINED |
    |  global.anthropic.claude-sonnet-4-6               |  Global Anthropic Claude Sonnet 4.6   |  SYSTEM_DEFINED |
    |  global.moonshotai.kimi-k3                        |  Global Moonshot AI Kimi K3           |  SYSTEM_DEFINED |
    |  jp.anthropic.claude-haiku-5-5                    |  JP Anthropic Claude Haiku 5.5        |  SYSTEM_DEFINED |
    |  global.anthropic.claude-haiku-5-5                |  Global Anthropic Claude Haiku 5.5    |  SYSTEM_DEFINED |
    |  global.anthropic.claude-sonnet-5-5               |  Global Anthropic Claude Sonnet 5.5   |  SYSTEM_DEFINED |
    |  global.openai.gpt-5.6-luna                       |  Global OpenAI GPT-5.6 Luna           |  SYSTEM_DEFINED |
    |  global.openai.gpt-6-luna                         |  Global OpenAI GPT-6 Luna             |  SYSTEM_DEFINED |
    +---------------------------------------------------+---------------------------------------+-----------------+
