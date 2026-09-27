# vpsdash

Painel de operação para um único operador: saúde de hosts e projetos, sessões
tmux, runners do gh-agents e troca de backend pelo mesmo `AGENT_RUNNER` /
`CI_RUNNER` que os workflows leem. Acesso público por HTTPS e senha em
`https://app.intrador.tech/`; Tailscale Serve continua como rota privada.

## Estado da implementação

O binário Go serve a API e a SPA React compilada, sem Node em produção.
SQLite guarda 30 dias de métricas e checks, estado corrente de runners e
sessões, e até 90 dias de alertas enviados. A descoberta de projetos cria
candidatos silenciosos; somente projetos promovidos são verificados e podem
gerar alerta após três falhas. Runners e fila vêm da API do GitHub App. O
switch altera as variáveis Actions do próprio repositório.
As units `actions.runner.*` e `gh-agents-cleanup.timer` da conta `gh-agents`
entram automaticamente como projetos monitorados, sem promoção manual.

O canal SMTP mostra "não provisionado" e mantém eventos pendentes enquanto
`smtp.env` não existir. A lista tmux detecta o agente; o terminal web e o
attach ficam para `v0.2`, conforme a especificação. A interface não afirma
que um host está saudável antes da primeira leitura.

O painel pode ser instalado na tela inicial como PWA pelo navegador. O service
worker guarda somente a interface estática; chamadas
`/api/` continuam na rede e nunca são servidas do cache. Sem conexão, o
painel não apresenta uma leitura antiga como estado atual.

## Desenvolver

Requer Go 1.27.1 e Node 22 para o build. Node não é usado pelo serviço. A
ponte SSH de leitura requer Python 3 nos hosts Linux monitorados.

```bash
cd web && npm ci && npm run build && cd ..
go test ./...
go build -trimpath -o bin/vpsdash ./cmd/vpsdash
bin/vpsdash --version
```

O build do Vite escreve `internal/web/dist/`; esses arquivos são incorporados
no binário por `embed.FS`. Para desenvolvimento, `npm run dev` em `web/`
encaminha `/api` para `127.0.0.1:8484`.

## Configuração do host

### Assistente de instalação

No terminal de `helio` na VPS, execute:

```bash
sudo scripts/setup-wizard.sh
```

O assistente compila e instala o serviço, pede a senha do painel sem eco,
configura os coletores SSH locais e remotos, grava o secret
`OPENCODE_API_KEY` no repositório, acompanha o registro e a instalação do
GitHub App, testa TLS e autenticação SMTP e publica o painel por Tailscale
Serve. Ele preserva credenciais já existentes e pode ser executado de novo
se alguma etapa ficar pendente. Ele lê as VPSs remotas do inventário privado
e identifica o host local pelo DNS do Tailscale; ajuste `config.json` antes
de executá-lo para uma frota diferente da configuração de exemplo.
Se o host local ainda não estiver no inventário, o assistente o inclui como
VPS após verificar a coleta SSH local. Antes de iniciar a unit, ele valida o
inventário e mostra o caminho do arquivo caso haja um erro.
Mesmo no host local, a coleta passa pela ponte SSH restrita da conta `helio`;
executar o coletor como `vpsdash` com `local: true` impediria a leitura das
sessões e serviços isolados nessa conta.
No exemplo, as VPSs remotas começam como presença Tailscale e só passam a
`vps` após a chave SSH ser verificada. O caminho de chave no inventário
preserva essa intenção para uma nova execução do assistente.

Para os hosts remotos, obtenha o fingerprint Ed25519 no console do próprio
host antes de informá-lo ao assistente. Nesse console, execute
`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256` e copie somente
o trecho `SHA256:...` da saída. Se não tiver acesso ao console naquele momento,
pressione Enter no campo do fingerprint para manter apenas a presença
Tailscale desse host e avance; você poderá executar o assistente novamente.
Endereços reais da tailnet, chaves e senhas ficam apenas na configuração
privada da conta `vpsdash` ou nos GitHub Actions secrets.

Se o terminal da VPS não tiver navegador, o assistente mostra o comando
`scp` para copiar o formulário de registro do App para seu computador.
Preencha-o no navegador local e cole no terminal apenas o código de uso
único retornado pelo GitHub. Nunca envie senhas nem esse código no chat.
Se o SMTP ainda estiver inacessível, o assistente indica a pendência e o
painel mantém alertas na fila até a próxima execução.

### Instalação manual

Instale o serviço sob o usuário próprio `vpsdash`, separado de `helio` e
`gh-agents`. O script cria o usuário e o linger, instala o binário e a unit,
recarrega o gerenciador systemd do usuário e verifica o resultado. Ele não
inicia o painel antes da configuração:

```bash
go build -trimpath -o bin/vpsdash ./cmd/vpsdash
sudo scripts/install-service.sh bin/vpsdash
```

O script pode ser executado novamente após um novo build; ele atualiza o
binário e a unit sem substituir `config.json` nem iniciar o serviço.

Edite `config.json`: substitua os domínios fictícios `.example.invalid`, ajuste
usuários SSH e liste somente repositórios instalados no GitHub App. Defina
`tailscale_serve_host` com o DNS do próprio host mostrado por
`tailscale status --json` em `Self.DNSName`. O assistente preenche esse campo
automaticamente. Defina
`agent_switchable` ou `ci_switchable` como `true` apenas depois de verificar
que o workflow daquele repositório contém `vars.AGENT_RUNNER || ...` ou
`vars.CI_RUNNER || ...`, respectivamente. Repositórios sem esse contrato não
recebem o controle de troca no painel.

Confira o inventário antes de iniciar o serviço:

```bash
sudo -u vpsdash /home/vpsdash/bin/vpsdash check-config --config /home/vpsdash/.config/vpsdash/config.json
```

Uma URL de health pode apontar para um endereço público ou para o nome DNS
de um host presente no inventário. Destinos privados só são aceitos quando
o hostname consta do inventário; redirecionamentos para outro host são
recusados. A regra vale ao salvar e na coleta. Para serviços internos, use
o DNS da tailnet do host ou o check de serviços esperados. Se o DNS não
responder ao salvar, a URL bem formada é aceita; a coleta só confirma o
estado após resolver e validar o destino.

Como `vpsdash`, gere a senha e a chave de sessão:

```bash
sudo -iu vpsdash /home/vpsdash/bin/vpsdash init-auth
```

O comando pede a senha no terminal e escreve `auth.env` com modo `0600`.
Para o host local, instale a ponte SSH de leitura como `helio` ou com `sudo`:

```bash
scripts/setup-local-collector.sh
```

O script gera uma chave Ed25519 exclusiva deste host, autoriza somente os
comandos de métricas, descoberta, sessões e health em `helio`, confere a host
key local e testa uma leitura real. Ele pode ser executado novamente sem
duplicar a autorização. Para outros hosts Linux, instale
`scripts/ssh-readonly.py` na conta SSH remota, gere uma chave Ed25519 diferente
por host e autorize a chave pública correspondente com
`restrict,command="/usr/bin/python3 -I /caminho/ssh-readonly.py"`.
Confirme a host key Ed25519 de cada servidor fora da conexão antes de
adicioná-la ao `known_hosts` de `vpsdash`; ajuste `ssh_user` e `ssh_key_file`
no inventário.
Para instalar e testar a ponte em um host remoto, use:

```bash
sudo scripts/setup-remote-collector.sh <host-id> <usuario-ssh> <fingerprint-SHA256>
```

O painel executa cada coleta com timeout de 10 s. Windows entra apenas pela
presença Tailscale, sem SSH.

Para monitorar as units do `gh-agents` no host local, configure uma segunda
chave, exclusiva da conta da frota. Execute como `helio` ou com `sudo`; o
script pede sudo quando necessário,
instala a ponte SSH com autorização apenas para a leitura das units, confere
a host key e testa uma coleta pela própria chave do `vpsdash`. Ele também
deixa em `/tmp` uma amostra dos estados com os nomes dos runners ocultados,
para substituir `internal/collect/testdata/runner_units.txt` após a primeira
instalação. Use o caminho impresso pelo script e rode `go test ./internal/collect`
depois da substituição:

```bash
scripts/setup-runner-collector.sh
```

Mantenha `runner_unit_hosts` no inventário com `ssh_user: "gh-agents"` e
`ssh_key_file` apontando para essa segunda chave. O painel usa a conta da
frota para ler `systemctl --user` a cada 30 s. Uma falha de SSH ou do bus
systemd aparece como erro do coletor; não conta como falha de uma unit.

Inicie o serviço:

```bash
vpsdash_uid="$(id -u vpsdash)"
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user daemon-reload
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user enable --now vpsdash.service
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user status vpsdash.service
```

Antes de publicar na tailnet, confira a configuração atual de Serve com
`tailscale serve status`. O backend aceita somente loopback. Configure HTTPS
privado para `127.0.0.1:8484` e verifique o domínio `*.ts.net` no navegador:

```bash
sudo tailscale serve --bg 127.0.0.1:8484
```

Se outro serviço já escutar na porta 443, use
`sudo tailscale serve --https=8443 --bg 127.0.0.1:8484` e acesse
`https://<nome-do-host>.<tailnet>.ts.net:8443/`. O assistente escolhe essa
porta automaticamente e aguarda a emissão inicial do certificado HTTPS.

Use **Serve**, não Funnel. O cookie de sessão exige HTTPS. A sintaxe de
`tailscale serve` segue a [documentação oficial](https://tailscale.com/docs/reference/tailscale-cli/serve).

### Acesso público com login

Para servir o painel em um domínio público, mantenha o binário escutando
somente em `127.0.0.1:8484` e configure um proxy HTTPS no mesmo host. Exemplo
de bloco Caddy (troque o domínio pelo seu):

```caddyfile
app.example.com {
    encode gzip zstd
    header Strict-Transport-Security "max-age=31536000"
    reverse_proxy 127.0.0.1:8484 {
        # Necessário para trust_proxy_header=true no inventário do exemplo.
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
    }
}
```

O proxy deve sobrescrever `X-Real-IP` e `X-Forwarded-For` com o IP da conexão
recebida; não encaminhe um valor fornecido pelo navegador. O inventário de
exemplo habilita `"trust_proxy_header": true` para que o login público tenha
limites por cliente. O assistente preserva uma escolha explícita diferente;
mantenha a opção ativa somente com um proxy público que sobrescreva ambos os
cabeçalhos. A rota do Tailscale Serve funciona com a opção ligada ou desligada.
Com `tailscale_serve_host` configurado, o serviço identifica primeiro o IP da
tailnet em `X-Forwarded-For`, que o Tailscale Serve sobrescreve. Ele usa esse
IP mesmo se um cliente alterar `Host` e enviar um `X-Real-IP` falso.
Assim, as duas rotas compartilham a porta local e mantêm limites por cliente.
O login limita cinco falhas por IP e
20 falhas globais em cinco minutos, além de duas verificações simultâneas.
Se outro proxy estiver à frente do Caddy, configure a cadeia de proxies
confiáveis antes de usar o IP do cliente. A página de login é pública;
as APIs de operação exigem sessão assinada. Confira a rota após a instalação:

```bash
curl -I https://app.example.com/
curl -i https://app.example.com/api/dashboard
```

A segunda chamada, sem cookie, deve retornar `401`. A rota Tailscale Serve
pode coexistir com a pública; não use Tailscale Funnel para expor outra rota.

## GitHub App `gh-agents-ops`

`app-manifest.json` pede `actions:read`, `administration:write` e
`actions_variables:write`. Não pede permissões de escrita na organização,
`contents` nem `issues`.
Metadata é implícita. Sem webhook ativo.

Se o App `gh-agents-ops` já foi registrado com uma versão anterior do
manifesto, remova `organization_self_hosted_runners` e
`organization_actions_variables` em **Settings → Developer settings → GitHub
Apps → gh-agents-ops → Permissions & events → Organization permissions**,
selecionando **No access** para ambas. [O GitHub aplica a remoção
imediatamente](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration).
Alterar o manifesto local não reduz as permissões de um App já registrado.

O fluxo de manifesto do GitHub exige uma confirmação no navegador. Em uma
máquina com navegador e este checkout:

```bash
python3 scripts/manifest-form.py > /tmp/vpsdash-app-form.html
xdg-open /tmp/vpsdash-app-form.html
```

Revise o manifesto no GitHub e crie o App na conta `heliowap`. O navegador
volta ao repositório com `?code=...` na URL. Troque esse código de uso único
em até uma hora, **sem imprimir a resposta**:

```bash
scripts/convert-app-manifest.sh
```

O script lê o código sem eco, verifica o acesso `sudo` antes da troca, chama
`POST /app-manifests/{code}/conversions` e instala o PEM e o
`github-app.env` como `vpsdash`, modo `0600`. Instale o App nos repositórios
desejados, obtenha os IDs de instalação e edite `GITHUB_INSTALLATIONS_JSON`
com `sudoedit /home/vpsdash/.config/vpsdash/github-app.env`, por exemplo
`{"heliowap":123456}`. Reinicie o serviço.
Tokens de instalação são gerados por request, mantidos em memória por até
50 minutos e nunca persistidos. As instalações nas organizações `intrador`
e `All-Medical` aguardam o rollout delas no gh-agents.

## SMTP

O arquivo privado `/home/vpsdash/.config/vpsdash/smtp.env` aceita
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_TO` e
`SMTP_AUTH=plain|login`. O serviço exige STARTTLS na porta 587 ou TLS
implícito na 465. Antes de provisionar, confirme porta, TLS, mecanismo de
autenticação e crie `vpsdash@intrador.com.br` em `allmedical-mail`. Alertas
pendentes permanecem na tabela `alerts` com `sent_at=0`.

Em 2026-09-26, o peer apareceu online na tailnet, mas conexões às portas 587
e 465 expiraram a partir do `intrador-tech-vps`. Verifique o serviço e o
firewall no host de e-mail antes de configurar `smtp.env`.

## gh-agents

`.github/workflows/agents.yml` chama
`heliowap/gh-agents/.github/workflows/agents.yml@v1` desde o primeiro PR.
O tag maior `@v1` é o contrato de release do workflow reutilizável e só é
movido em uma release deliberada do `gh-agents`; as actions externas dentro
dele continuam pinadas por SHA. O caller passa
`vars.AGENT_RUNNER || 'ubuntu-latest'` e o callee respeita essa variável.
Defina `OPENCODE_API_KEY` no repositório. Até haver um runner privado
registrado para este repositório, defina `AGENT_RUNNER=ubuntu-latest`; depois
o painel pode trocar para `self-hosted`. O CI usa
`vars.CI_RUNNER || 'ubuntu-latest'`.

## Verificar

```bash
cd web && npm ci && npm run build && cd ..
go test ./...
go test -race ./...
go vet ./...
actionlint .github/workflows/*.yml
shellcheck scripts/*.sh
test -z "$(git status --porcelain -- internal/web/dist)"
go build -o /tmp/vpsdash ./cmd/vpsdash
/tmp/vpsdash --version
```

Os parsers têm fixtures de formato Tailscale com nomes e domínios fictícios.
Um teste de navegador do fluxo login → hosts → projetos
está em `web/tests/overview.plan.json` para execução TestSprite com
`--local 8484`. Chaves, senha, IDs de instalação e endereços reais ficam
fora do repositório.
