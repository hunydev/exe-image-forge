# hunyimg

exe.dev 호환 커스텀 이미지를 만들고, **암호 인증 후 일정 시간만 유효한 이미지 경로**를 발급하는 시스템.

## 구성

```
브라우저 ──HTTPS──▶ exe.dev proxy ──▶ :8000 vend (Go)
                                       ├── /                발급 페이지 (자격증명 경고 표시)
                                       ├── /admin/          관리 페이지 (웹 터미널 + bake)
                                       ├── /api/grant        비밀번호 → 임시 토큰 경로 발급
                                       └── /v2/t/<token>/…   토큰 스코프 registry 프록시
                                                  │
                                                  ▼
                                          :5000 registry:2 (localhost only)
```

- `hunydev/base` — node/npm/npx, python, uv/uvx, go, gh, codex, claude, gemini + systemd/exeuntu 호환 레이어
- `hunydev/dev` — base + 로그인된 자격증명을 구운 이미지
- 발급 시 `LABEL`만 다른 얇은 레이어를 새 태그로 push → 태그별 digest가 고유하므로 만료 시 개별 삭제 가능
- 만료되면 태그·토큰 경로 삭제, blob은 매일 `hunyimg-gc.timer`가 회수

## 웹에서 전체 관리 (`/admin/`)

메인과 **동일한 암호**로 로그인합니다. SSH 없이 브라우저만으로 끝낼 수 있습니다.

1. **자격증명 상태표** — 서비스별 만료 시각을 계산해 남은 시간/경과 시간으로 보여줍니다.
   - `정상` — 유효
   - `액세스 토큰 만료` (노랑) — refresh 토큰이 있어 CLI가 첫 실행 시 자동 갱신. 보통 조치 불필요
   - `만료됨` / `확인 불가` (빨강) — 재로그인 필요
   - `로그인 안됨` (회색) — 아직 OAuth 미완료
2. **웹 터미널** — 베이스 이미지 안에서 도는 실제 PTY(xterm.js ↔ websocket ↔ `docker run -it`).
   표의 행이나 `gh`/`codex`/`claude`/`gemini` 버튼을 누르면 해당 로그인 명령이 바로 입력됩니다.
   동시 세션은 1개로 제한되며(자격증명 경합 방지), 소켓이 끊기면 컨테이너도 정리됩니다.
3. **Gemini 콜백 릴레이** — 아래 "localhost 리다이렉트" 참고.
4. **패스키 (WebAuthn)** — 암호 외에 패스키로도 로그인할 수 있습니다. 등록은 관리 페이지에서 하고,
   등록된 패스키가 있으면 로그인 화면에 "패스키로 로그인" 버튼이 나타납니다.
   패스키는 **도메인에 고정**되므로 `hunydev-images.exe.xyz` 와 `images.huny.dev` 각각 등록해야 합니다
   (WebAuthn 명세상 자격증명이 RP ID 에 묶입니다). 목록에는 다른 도메인의 키도 보이며 삭제할 수 있습니다.
   discoverable credential(resident key)을 요구하므로 사용자명 입력 없이 바로 인증됩니다.
5. **bake 버튼** — 웹에서 `hunydev/dev:latest` 를 굽고 로그를 실시간으로 스트리밍합니다.

메인 페이지는 발급 **전에** 문제를 알려줍니다. 만료·미로그인 서비스가 있으면 빨간 배너와
관리 페이지 바로가기가 뜨므로, 낡은 이미지를 모르고 새 VM에 배포하는 일을 막습니다.
인증 없이 노출되는 정보는 상태 요약뿐이고, 계정명·파일 경로는 로그인 후에만 보입니다.

## localhost 리다이렉트 회피

원격 VM 에서 OAuth 를 할 때의 핵심 문제입니다. CLI 가 `http://localhost:<포트>` 로 리다이렉트하면
그 주소는 **당신 노트북의 localhost** 로 해석되므로, VM 에서 대기 중인 리스너에 절대 닿지 않습니다.
CLI 별로 대응이 다릅니다:

| CLI | 방식 | localhost 사용? |
| --- | --- | --- |
| `gh auth login` | device code | 안 함 |
| `codex login --device-auth` | device code (`auth.openai.com/codex/device` + 1회용 코드) | 안 함 |
| `claude setup-token` | `platform.claude.com` 로 리다이렉트 후 코드 붙여넣기 | 안 함 |
| `gemini` | **device code 없음** — 항상 임의 포트 localhost 리스너 | **함 → 릴레이 필요** |

앞의 세 개는 그냥 버튼만 누르면 됩니다. `codex login` (플래그 없이) 은 `localhost:1455` 를 쓰므로
쓰지 마세요 — UI 와 CLI 모두 `--device-auth` 를 강제합니다.

Gemini 만 릴레이가 필요합니다. 브라우저에서 승인하면 "연결할 수 없음" 페이지로 떨어지는데,
그 **주소창의 URL 전체**를 복사해서:

- 관리 페이지의 릴레이 입력창에 붙여넣기, 또는
- `hunyimg relay '<URL>'`

VM 안에서 대신 요청하므로 CLI 가 콜백을 받아 로그인이 완료됩니다.
릴레이는 `localhost`/`127.0.0.1` + 비특권 포트만 허용합니다 — 그렇지 않으면 이 엔드포인트가
VM 내부망을 향한 SSRF 도구가 됩니다. 이 거부 규칙은 테스트로 고정돼 있습니다.

## CLI 사용 흐름

```sh
hunyimg build                  # 베이스 이미지 빌드
hunyimg auth gh                # 이미지 안에서 로그인 (또는 hunyimg import)
hunyimg status                 # 자격증명 확인
hunyimg bake                   # hunydev/dev:latest 생성
```

자격증명은 `/var/lib/hunyimg/authhome` 에 보존됩니다. 이미지를 재빌드해도 유지되고, `bake` 할 때만 이미지에 들어갑니다.

### 랩톱에서 이미 로그인돼 있다면

```sh
hunyimg export-hint    # 랩톱에서 실행할 tar 명령을 출력
```

## 명령어

| 명령 | 설명 |
| --- | --- |
| `hunyimg build` | 베이스 이미지 재빌드 |
| `hunyimg auth {gh\|codex\|claude\|claude-token\|gemini\|all}` | 이미지 안에서 로그인 |
| `hunyimg import [file]` | tar 로 자격증명 반입 (기본 stdin) |
| `hunyimg export-hint` | 랩톱에서 쓸 export 명령 출력 |
| `hunyimg shell` | 영속 HOME 을 붙인 대화형 셸 |
| `hunyimg status` | 자격증명 존재 여부 |
| `hunyimg bake` | `hunydev/dev:latest` 생성 |
| `hunyimg verify [img]` | 툴/인증 점검 |
| `hunyimg password` | 웹 비밀번호 변경 |
| `hunyimg token [tok]` | 랩톱 `docker pull` 용 VM 토큰 저장 |
| `hunyimg vend-build` | vend 서비스 재빌드·재시작 |
| `hunyimg grants` | 활성 grant 목록 |
| `hunyimg gc` | registry/도커 가비지 수거 |

## HTTPS 관련 주의

exe.dev 프록시는 HTTPS 만 노출하며 기본이 **인증 게이트(private)** 입니다.

- `ssh exe.dev new --image=…` 는 exe.dev 내부에서 이미지를 당기므로, 프록시가 private 이면 실패합니다.
  `ssh exe.dev share set-public hunydev-images` 로 공개해야 합니다.
- 공개로 두더라도 발급된 토큰 경로를 모르면 아무 것도 볼 수 없습니다. `/v2/` ping 만 `{}` 를 응답하고,
  `/v2/t/<token>/…` 은 유효 토큰·해당 repo·GET/HEAD 로만 제한됩니다.
- 랩톱에서 직접 `docker pull` 하려면 private 상태에서도 VM 토큰을 basic-auth 비밀번호로 쓸 수 있습니다:
  `ssh exe.dev ssh-key generate-api-key --vm=hunydev-images --label=registry` → `hunyimg token <TOKEN>`

## 보안

- 비밀번호는 PBKDF2-HMAC-SHA256 (210k iters, 랜덤 salt) 로 `/etc/hunyimg/config.json` 에 저장
- 5회 실패 후 지수적 lockout (발급/관리 로그인 공유)
- 관리 세션은 HttpOnly·Secure·SameSite=Lax 쿠키, 8시간 만료
- 패스키(WebAuthn)는 암호와 동등한 로그인 수단. RP ID 는 접속 호스트에서 유도하고 origin 을 검증하므로
  다른 도메인의 키로는 인증되지 않습니다. 서명 카운터를 저장해 복제 인증기를 거부합니다.
  패스키 실패도 암호와 같은 lockout 카운터를 공유합니다.
- 터미널·bake·자격증명 상세는 모두 세션 필수 (미인증 시 401)
- 발급 토큰은 128비트 랜덤, 읽기 전용, 단일 repo 스코프, TTL 최대 24h
- backing registry 는 `127.0.0.1:5000` 만 바인딩되어 외부에서 직접 접근 불가

## 파일 위치

- 코드: `/home/exedev/hunyimg`
- 설정: `/etc/hunyimg/config.json`
- 상태: `/var/lib/hunyimg/{grants.json,authhome,registry}`
- 서비스: `hunyimg-vend.service`, `hunyimg-gc.timer`, `registry` (docker)


## 자동 업데이트

AI agent CLI 는 두 군데서 자동으로 갱신됩니다.

**VM 안에서** — `ai-cli-update.timer` 가 부팅 3분 후 한 번, 이후 매일 `update-ai-clis` 를 실행합니다.
부팅 트리거가 있어야 오래된 이미지로 만든 VM 도 바로 최신이 됩니다. 릴리스 서버에 몰리지 않도록
30분 지터를 넣었습니다.

**이미지 자체** — 이 VM 의 `hunyimg-refresh.timer` 가 매주 일요일 `hunyimg refresh` 를 돌려
base 를 다시 빌드하고 (자격증명이 있으면) dev 를 다시 굽습니다. 자격증명은 이미지 밖
(`/var/lib/hunyimg/authhome`)에 있으므로 무인 실행이 안전합니다.

`update-ai-clis` 설계:

- 도구별로 독립 실행 — 한 곳의 네트워크 오류가 나머지를 막지 않습니다 (`set -e` 를 쓰지 않음)
- 새 바이너리를 임시 위치에 받아 `--version` 이 도는지 확인한 뒤에야 교체 (중단되어도 실행 불가 상태가 되지 않음)
- 결과를 `/etc/ai-cli-versions.json` 에 기록 → 관리 페이지 "도구 버전" 카드와 `hunyimg versions` 가 이걸 읽습니다
- Dockerfile 도 같은 스크립트로 설치하므로 "최신 버전 구하는 법" 정의가 한 곳뿐이고, 업데이트 경로가 매 빌드마다 검증됩니다

## 이미지 구성 (variant)

무거운 선택 항목은 **홈 화면에서 이미지를 요청할 때** 체크박스로 고릅니다. 둘 다 기본은 **꺼짐**입니다.

| 구성 | 태그 | 다운로드 | 추가 |
|---|---|---|---|
| 최소 (기본) | `min` | 497MB | — |
| + Gemini CLI | `gemini` | 517MB | +21MB |
| + Go 툴체인 | `go` | 559MB | +62MB |
| + 둘 다 | `go-gemini` | 580MB | +83MB |

항상 포함: codex · claude · gh · node/npm/npx · python · uv/uvx · git · build-essential.

네 구성 모두 미리 빌드해 두고, 요청 시 해당 variant 를 토큰 경로로 발급합니다.
**선택 항목은 Dockerfile 의 마지막 레이어**라서 네 구성이 그 아래 레이어(codex+claude 587MB 포함)를
전부 공유합니다 — 레지스트리에 이 blob 이 한 번만 저장됩니다. 순진하게 앞쪽에 뒀다면 구성마다
587MB 를 중복 저장했을 겁니다. 이 순서는 테스트로 고정해 뒀습니다.

표의 크기는 **압축 크기**(= 레지스트리 매니페스트의 레이어 합계 = 실제 `docker pull` 전송량)입니다.
`docker images` 가 보여주는 2.1GB 는 압축을 푼 뒤의 디스크 사용량입니다.

`min` 이미지에서는 업데이트 타이머가 gemini 를 **되살리지 않습니다**. 작은 이미지를 고른 선택이
첫 부팅 때 조용히 뒤집히면 안 되기 때문입니다.

```
hunyimg build [--fresh] [variant]   네 구성 모두(또는 하나만) 빌드
hunyimg bake [variant]              자격증명을 각 구성 위에 굽기
hunyimg sizes                       구성별 크기
hunyimg versions [image]            구성별 도구 버전
```


## AI 에이전트 환경 인지

이미지에 무엇이 설치돼 있고 어떤 환경인지, 각 CLI 의 **전역 지침 파일**에 자동으로 기록됩니다.

| CLI | 파일 |
|---|---|
| codex | `~/.codex/AGENTS.md` |
| claude | `~/.claude/CLAUDE.md` |
| gemini | `~/.gemini/GEMINI.md` |

내용: OS/커널/CPU/RAM/디스크, 사용자와 sudo, 설치된 툴체인과 버전, AI CLI 버전,
**HTTPS 만 노출된다는 점과 localhost OAuth 콜백이 안 되는 이유**, 자격증명이 이미 구워져 있다는 점,
자동 업데이트가 돌고 있으니 수동 업그레이드하지 말 것 등.

설계상 중요한 점 두 가지:

**구성별로 내용이 다릅니다.** `min` 이미지의 지침에는 Go 와 Gemini 가 "설치되지 않음" 으로
명시되고, 손으로 설치하지 말고 해당 구성 이미지를 요청하라고 안내합니다. 정적인 목록을 넣었다면
에이전트가 없는 도구를 있다고 믿고 계획을 세웠을 겁니다.

**부팅할 때마다 다시 생성됩니다** (`agent-context.service`). 커널 버전, vCPU 수, 디스크 크기는
이미지를 빌드한 호스트가 아니라 실제로 부팅된 VM 의 값이어야 합니다. 빌드 시점에 한 번만 썼다면
자신 있게 틀린 정보를 말했을 겁니다.

생성 블록은 마커로 감싸여 있어서, 사용자가 같은 파일에 추가한 내용은 재생성해도 보존됩니다.

```
hunyimg context [variant]   # AI CLI 가 읽게 될 내용 확인
```
관리 페이지의 "AI 에이전트 컨텍스트" 카드에서 구성별로 확인할 수 있습니다.


## 로그인 명령 주의

| CLI | 명령 | 비고 |
|---|---|---|
| gh | `gh auth login --git-protocol https` | device code |
| codex | `codex login --device-auth` | device code |
| claude | **`claude auth login`** | platform.claude.com 리다이렉트 + 코드 붙여넣기 |
| gemini | **`NO_BROWSER=true gemini`** | codeassist.google.com 리다이렉트 + 코드 붙여넣기 |

**`claude setup-token` 을 쓰면 안 됩니다.** 이 명령은 토큰을 화면에 출력만 하고
(`export CLAUDE_CODE_OAUTH_TOKEN=<token>` 용) 디스크에 아무것도 저장하지 않습니다.
로그인은 성공한 것처럼 보이지만 `~/.claude/.credentials.json` 이 생기지 않아 웹 UI 가
계속 "로그인 안 됨" 으로 표시하고, bake 해도 자격증명이 들어가지 않습니다.
자격증명을 실제로 저장하는 것은 `claude auth login` 입니다.

관리 터미널과 `hunyimg auth` 는 **`hunydev/base:go-gemini`** 에서 실행됩니다.
기본 구성(`min`)에는 gemini 가 없어서 로그인 셸에서 `gemini: command not found` 가 납니다.
자격증명은 구성과 무관하게 `$AUTHHOME` 하나에 모입니다.


## bake 는 자격증명만 담습니다

관리 터미널에서 AI 에이전트에게 프롬프트를 입력하면 그 내용이 `$AUTHHOME` 에 남습니다:

- `.gemini/tmp/<project>/logs.json`, `.gemini/tmp/<project>/chats/*.jsonl` — 프롬프트 전문
- `.codex/state_*.sqlite`, `.codex/logs_*.sqlite` — 세션·스레드
- `.bash_history` — 터미널에서 친 명령
- `.claude.json` — 계정 ID, 프로젝트 목록

예전 bake 는 `$AUTHHOME` 전체를 tar 로 묶고 몇 개만 `--exclude` 했기 때문에 **이것들이 전부
배포 이미지에 들어갔습니다.** 이미지를 받아간 사람이 읽을 수 있는 상태였습니다.

지금은 `CRED_FILES` **화이트리스트**에 있는 파일만 복사합니다. 빠뜨린 것이 유출되는 게 아니라,
빠뜨린 것은 그냥 안 들어갑니다. bake 할 때마다 담기는 파일 목록을 출력합니다.

```
==> baking 10 credential file(s):
  /.codex/auth.json
  /.claude/.credentials.json
  /.gemini/gemini-credentials.json
  ...
```

## Gemini 자격증명은 호스트에 묶여 있습니다

Gemini CLI 0.53 은 토큰을 `~/.gemini/gemini-credentials.json` 에 **암호화**해 저장하는데,
키가 `scrypt(hostname + username)` 입니다. 이미지가 다른 호스트에서 부팅되면 복호화가 불가능해서
**파일을 그대로 복사하면 로그인이 안 넘어갑니다.**

그래서 bake 는 로그인한 이 호스트에서 `image/files/gemini-export-creds.js` 로 먼저 변환합니다:

- OAuth 로그인 → 평문 `oauth_creds.json` (CLI 가 기본으로 읽는 형식)
- API 키 로그인 → 이미지에 `ENV GEMINI_API_KEY=...`

`.gemini/oauth_creds.json` 만 찾던 예전 코드가 로그인을 인식하지 못한 것도 같은 이유입니다.
지금은 두 형식을 모두 인식합니다.


## base 와 dev 를 헷갈리지 마세요

| 리포지토리 | 내용 |
|---|---|
| `hunydev/dev` | 도구 + **로그인된 자격증명** ← 이걸 받으세요 |
| `hunydev/base` | 도구만. 자격증명 없음 (bake 의 재료) |

`hunydev/base` 를 받으면 `gh auth status`, codex, claude, gemini 모두 **로그인이 안 된 상태**입니다.
버그가 아니라 설계입니다 — base 는 자격증명이 들어가기 전 단계입니다.

예전에는 홈 화면 드롭다운에 `hunydev/base` 가 먼저 떠서 기본 선택이었고, 이름만으로는 차이를
알 수 없었습니다. 지금은 `hunydev/dev` 가 기본이고 두 항목 모두 라벨로 구분됩니다.

bake 는 끝나고 실제로 로그인이 넘어갔는지 확인합니다:

```
==> verified: all four logins survive into hunydev/dev:min
```

넘어가지 않았으면 `warn: baked image: <tool> is NOT logged in` 이 뜹니다.


## 이미지 크기

레이어는 gzip 이 아니라 **zstd** 로 압축합니다. 같은 내용이 20% 작고, 무엇보다 압축 해제가
훨씬 빠릅니다 — 바이트가 도착한 뒤 기다리는 시간의 대부분이 이겁니다.

| | gzip | zstd -9 |
|---|---|---|
| min 다운로드 | 497MB | **399MB** |
| codex 바이너리 압축 해제 | 1942ms | **354ms** |
| 콜드 pull (루프백) | 14.0s | **10.7s** |

`force-compression=true` 가 필수입니다. 없으면 buildx 가 부모 이미지의 gzip 레이어를 그대로
통과시켜서, 설정은 걸려 있는데 아무 효과가 없습니다.

`COMPRESSION=gzip hunyimg build` 로 되돌릴 수 있습니다.

### 압축 해제 후 1.5GB 는 왜인가

| | 크기 |
|---|---|
| codex + claude 바이너리 | 559MB |
| gcc 툴체인 (build-essential) | 205MB |
| node + npm | 139MB |
| uv / gh / python3 | 145MB |

`codex`(297MB) 와 `claude`(262MB) 가 절반 이상인데 둘 다 이미 strip 된 정적 바이너리라
줄일 방법이 없습니다. claude 는 Node 런타임을 통째로 품은 SEA 라서 우리가 설치한 `node` 와
중복이지만, npm 배포판도 같은 바이너리를 내려받으므로 이득이 없습니다.

문서·man·locale·apt 캐시는 이미 제거된 상태입니다.

줄일 수 있는 유일한 후보는 **build-essential (205MB, 압축 후 72MB)** 입니다. 빼면 min 이
399MB → 327MB, 압축 해제 1.5GB → 1.2GB 가 됩니다. 다만 네이티브 npm 모듈 빌드와 cgo 가
불가능해지므로 기본값으로는 유지합니다.
