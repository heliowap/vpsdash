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
Ao lado do switch, a Frota mostra minutos por backend (`self-hosted`,
`ubuntu-latest`, `depot-*`, `ubicloud-*`, outros) em 7 e 30 dias como proxy
de custo, não como cobrança. O coletor lê a cada 10 minutos os jobs de cada
tentativa de run concluída uma única vez, guarda 30 dias em `job_minutes` e
pausa ao restarem 500 requisições da API para preservar a frota e o switch.
As units `actions.runner.*` e `gh-agents-cleanup.timer` da conta `gh-agents`
entram automaticamente como projetos monitorados, sem promoção manual. Na
página Frota, cada unit de runner pode ser reiniciada ou drenada após
confirmação (veja [Operar units de runner](#operar-units-de-runner)).

O canal SMTP mostra "não provisionado" e mantém eventos pendentes enquanto
`smtp.env` não existir. Com `webpush.env`, cada dispositivo pode ativar
notificações push na visão geral (§Web Push). A lista tmux detecta o agente e
sugere seu estado (trabalhando, esperando input ou ociosa) a partir da tela e
da CPU do pane entre duas leituras. Pelo endereço privado da tailnet, a página
Sessões abre um terminal web (xterm.js sobre WebSocket até um PTY SSH), o
attach tmux somente leitura com um clique, a resposta a um agente que espera
input (send-keys, inclusive a partir da notificação push) e os comandos fixos
de cada host;
veja [Terminal, attach e comandos](#terminal-attach-e-comandos-rota-privada).
A interface não afirma
que um host está saudável antes da primeira leitura.

A aba Arquivos navega e exibe, somente para leitura, as pastas listadas em
`file_roots` de cada VPS no inventário (veja [Arquivos](#arquivos-somente-leitura)).
A leitura é opcional: o `config.example.json` não traz `file_roots`. Sem
`file_roots`, a leitura fica desativada e a aba não aparece.

O painel pode ser instalado na tela inicial como PWA pelo navegador. O service
worker guarda somente a interface estática e exibe as notificações push;
chamadas `/api/` continuam na rede e nunca são servidas do cache. Sem conexão, o
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
comandos de métricas, descoberta, sessões, health e leitura de arquivos nas
raízes do host em `helio`, confere a host
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

Cada script de coleta é aceito pela ponte pelo seu SHA-256. Quando um script
muda (por exemplo, a coleta de sessões), reinstale a ponte com
`scripts/setup-local-collector.sh` e `scripts/setup-remote-collector.sh`
antes de atualizar o binário; sem isso,
a ponte recusa a nova leitura e o painel marca as sessões como não
confirmadas.

O painel executa cada coleta com timeout de 10 s. Windows entra apenas pela
presença Tailscale, sem SSH.

### Arquivos (somente leitura)

A aba Arquivos lista pastas e exibe arquivos de texto dentro de raízes
explícitas. Nada é gravado, movido ou apagado. O acesso exige duas listas
que concordem:

1. `file_roots` do host no inventário do painel. O painel recusa qualquer
   caminho fora dessas raízes antes de contatar o host. Sem `file_roots`, a
   leitura fica desativada e a aba não aparece. O exemplo e a instalação
   padrão não definem o campo; para ativar, edite o inventário privado
   (`sudoedit /home/vpsdash/.config/vpsdash/config.json`) e acrescente o
   campo à VPS desejada:

   ```json
   { "id": "intrador-tech-vps", "tailnet_name": "sample-vps.example.invalid", "kind": "vps", "ssh_user": "helio",
     "ssh_key_file": "/home/vpsdash/.ssh/id_ed25519_intrador-tech-vps",
     "file_roots": ["/home/helio/Projetos"] }
   ```

   Confira com `sudo -u vpsdash /home/vpsdash/bin/vpsdash check-config
   --config /home/vpsdash/.config/vpsdash/config.json`.
2. O arquivo `~/.config/vpsdash-files/roots` da conta SSH no próprio host
   (uma raiz absoluta por linha, dono dessa conta ou root, sem escrita para
   grupo ou outros, inclusive na pasta). A ponte `ssh-readonly.py` lê as raízes
   somente desse arquivo; o painel não consegue ampliá-las pela conexão SSH.
   Para fixar outro caminho, acrescente `--file-roots /caminho/absoluto` ao
   `command=` da chave em `authorized_keys`.

Os scripts de coleta gravam esse arquivo quando recebem raízes como
argumentos adicionais; sem argumentos, mantêm o arquivo existente:

```bash
scripts/setup-local-collector.sh /home/helio/Projetos
sudo scripts/setup-remote-collector.sh <host-id> <usuario-ssh> <fingerprint-SHA256> /srv/app
```

Quando o inventário tem `file_roots` para um host, o assistente mostra as
pastas exatas e só grava o arquivo de raízes se você responder sim (o padrão
é não). Sem `file_roots` ou com a resposta não, nenhum arquivo de raízes é
criado ou alterado. Para
desativar a leitura em um host, remova `file_roots` do inventário e apague
`~/.config/vpsdash-files/roots` nesse host.

Regras aplicadas no host, pela ponte, e no painel para hosts `local: true`:

- O caminho precisa ser absoluto e normalizado: `..`, `.`, `//` e `/` final
  são recusados. Depois de resolver links simbólicos, o destino real precisa
  continuar dentro de uma raiz; o arquivo é aberto sem seguir links e a ponte
  confere o caminho aberto em `/proc/self/fd`.
- Nomes com aparência de segredo aparecem na listagem como **bloqueado**, sem
  tamanho nem data, e nunca são lidos: `.env*`, `*.env`, `*.pem`, `*.key`,
  `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, `*.kdbx`, `*.gpg`, `id_*`,
  `*secret*`, `*credential*`, `*password*`, `*passwd*`, `*_history`, `.netrc`,
  `.npmrc`, `.pypirc`, `.pgpass`, `.htpasswd`, `.vault-token` e qualquer
  caminho dentro de `.ssh`, `.gnupg`, `.git`, `.aws`, `.azure`, `.kube`,
  `.docker` ou `.password-store`. Links que apontam para fora da raiz também
  aparecem como bloqueados.
- Cada leitura devolve no máximo 512 KB. O painel informa **truncado** e
  carrega o trecho seguinte sob pedido. Conteúdo com byte nulo ou que não é
  UTF-8 aparece como **binário** e não é exibido. Pastas mostram até 1000
  itens e avisam quando há mais.

Com `local: true`, o próprio processo `vpsdash` lê os arquivos com as
permissões da conta `vpsdash` e usa somente o `file_roots` do inventário. Em
produção, prefira a ponte SSH, que lê com a conta do operador e mantém as
raízes fora do alcance do painel.

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

#### Operar units de runner

A mesma chave da conta `gh-agents` aceita dois comandos de escrita, e só eles:
`runner-restart <unit>` e `runner-drain <unit>`. A ponte exige um nome
`actions.runner.<nome>.service` cujo arquivo exista em
`~gh-agents/.config/systemd/user/`, executa `systemctl --user` com lista de
argumentos, sem shell e sem sudo, e recusa o timer de limpeza. A chave de
`helio` não recebe esses comandos. O GitHub App não ganha permissões.

- **Reiniciar** executa `systemctl --user restart`. O runner trata o SIGTERM
  como desligamento e cancela o job em andamento; a confirmação avisa quando
  o GitHub informa um job.
- **Drenar** espera o job atual terminar e então para a unit. O painel
  consulta a última leitura do GitHub a cada 15 s; quando ela não mostra
  job, pede a parada à ponte, que antes confere se ainda existe um processo
  `Runner.Worker` no cgroup da unit. Se existir, a unit continua ativa e o
  painel tenta de novo. A espera pode ser cancelada e termina em 2 h sem
  parar a unit. O runner não tem um modo que recuse novos jobs, e parar a
  unit é a única forma de impedi-los sem desregistrá-lo no GitHub; por isso
  um job atribuído nos segundos entre a última verificação e a parada pode
  ser cancelado.
- Uma unit drenada fica parada até **Iniciar** (o mesmo `restart`). Enquanto
  isso, as leituras continuam registradas, mas a parada planejada não gera
  alerta nem aparece como incidente. O mesmo vale enquanto uma operação do
  painel está em andamento e, por até 2 min depois de um reinício, enquanto
  o systemd ainda mostra a unit `activating`/`deactivating`. Essas leituras
  são neutras: não contam para as três falhas seguidas e interrompem a
  sequência, então uma falha real depois do reinício só alerta após três
  verificações falhas próprias.

Cada pedido é gravado na tabela `runner_unit_ops` (ação, resultado,
horários) e no log do serviço. Se o serviço reiniciar durante uma drenagem,
a operação fica registrada como interrompida.

Se a drenagem é cancelada, expira ou o serviço é encerrado enquanto o pedido
de parada já está a caminho do host, o painel não presume o resultado: lê o
estado da unit uma vez. Unit parada fecha a operação como drenagem
concluída (parada planejada, sem alerta); unit ativa mantém o cancelamento
com o estado lido. Se a leitura falhar, o registro diz "Estado da unit não
confirmado", a UI mostra "Não confirmado" fora dos incidentes, e a próxima
leitura das units decide o resultado da mesma forma.

Para atualizar um painel já instalado, reinstale a ponte **antes** de
publicar o novo binário; a versão anterior recusa os comandos novos:

```bash
scripts/setup-runner-collector.sh
```

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
`tailscale serve status`. O backend aceita somente loopback. O Serve deve
apontar para o listener privado (`private_listen`, `127.0.0.1:8485` no
exemplo), que é o único a servir terminal, attach e comandos. Configure HTTPS
privado para ele e verifique o domínio `*.ts.net` no navegador:

```bash
sudo tailscale serve --bg 127.0.0.1:8485
```

Se `private_listen` não estiver no inventário, o Serve pode continuar em
`127.0.0.1:8484`, mas o painel fica somente leitura também pela tailnet.
Se outro serviço já escutar na porta 443, use
`sudo tailscale serve --https=8443 --bg 127.0.0.1:8485` e acesse
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

O proxy deve sobrescrever **ambos** `X-Real-IP` e `X-Forwarded-For` com o mesmo
IP da conexão recebida; não encaminhe um valor fornecido pelo navegador. O
painel só usa `X-Real-IP` quando os dois cabeçalhos concordam. O inventário de
exemplo habilita `"trust_proxy_header": true` para que o login público tenha
limites por cliente. O assistente preserva uma escolha explícita diferente;
mantenha a opção ativa somente com um proxy público que sobrescreva ambos os
cabeçalhos. A rota do Tailscale Serve funciona com a opção ligada ou desligada.
O serviço identifica primeiro o IP da tailnet em `X-Forwarded-For`, que o
Tailscale Serve sobrescreve, mesmo se `tailscale_serve_host` não estiver
preenchido. Ele usa esse IP mesmo se um cliente alterar `Host` e enviar um
`X-Real-IP` falso.
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
O proxy público aponta **somente** para `listen` (`127.0.0.1:8484`); nunca
para `private_listen`.

### Terminal, attach e comandos (rota privada)

O painel tem dois listeners em loopback. `listen` recebe o proxy público e
serve login, leitura e o switch de runners. `private_listen` recebe apenas o
Tailscale Serve e acrescenta as rotas interativas: `/api/step-up`,
`/api/interactive`, `/api/terminal/tickets`, `/api/terminal/ws`,
`/api/hosts/{id}/snippets/run` e `/api/hosts/{id}/sessions/send-keys`. Essas rotas não existem no mux público, que
responde `404` mesmo com sessão válida; nenhum cabeçalho muda essa decisão.
Pela rota pública, a página Sessões explica que o terminal só está disponível
pelo endereço privado.

Antes de abrir um terminal, um attach ou executar um comando, o painel pede a
senha de novo. A confirmação vale 10 minutos, fica em memória ligada à
sessão atual e conta nos mesmos limites de tentativas do login. O terminal
abre por um ticket de uso único (30 s) obtido com CSRF e por um WebSocket
cuja `Origin` precisa ser igual ao `Host`. São até quatro sessões
interativas ao mesmo tempo; o terminal fecha após 15 minutos sem digitação.
A tabela `audit_log` guarda hora, ação, host, alvo, IP do cliente e
resultado de cada confirmação de senha, abertura, recusa, encerramento e
comando, por 90 dias. Teclas e saídas nunca são gravadas; a página Sessões
mostra os registros recentes.

O attach usa `tmux attach-session -r -t =<sessão>` (somente leitura) e só
aceita sessões observadas pela coleta naquele host. Assumir o controle é uma
segunda opção, com confirmação. "Terminal local" mostra o comando `ssh`
equivalente para colar no seu terminal, com a sua própria chave.

"Responder" abre, ao lado da sessão, respostas rápidas (`y`, `n`, `1`–`3`,
Enter, Esc, setas, Tab) e um campo de texto de uma linha (até 200
caracteres, sem caracteres de controle; Enter ao final só se você marcar).
Antes do envio, uma confirmação mostra sessão, host e as teclas exatas; se a
coleta não marca a sessão como esperando input, a confirmação avisa, mas não
bloqueia. O painel exige que a sessão conste da última coleta daquele host,
localiza o pane cujo processo a coleta observou e usa `tmux send-keys -l`
pela chave interativa, com cada argumento entre aspas simples. O resultado
fica escrito na linha: Enviado, Recusado ou Falhou. `C-c` não é oferecido. O
`audit_log` registra `send_keys` com host e sessão, nunca as teclas. A
notificação push de agente esperando input abre essa sessão já com a resposta
aberta; pela rota pública, a página só mostra a nota da tailnet.

O acesso interativo usa uma chave SSH por host, diferente de todas as chaves
de coleta. A ponte `ssh-readonly.py` continua igual. Crie e autorize a chave
para `helio` com `restrict,pty` (sem encaminhamento de porta, agente ou X11):

```bash
# host local, como helio ou com sudo
scripts/setup-interactive-key.sh local
# host remoto; fingerprint obtido no console do próprio host
sudo scripts/setup-interactive-key.sh <host-id> helio <fingerprint-SHA256>
```

O terminal roda com os direitos da conta de `ssh_user`, por isso um host com
`interactive_key_file` só é aceito com `ssh_user: "helio"`. O inventário e o
script recusam conta vazia, `root`, `gh-agents`, `vpsdash`, qualquer conta de
`runner_unit_hosts` e qualquer outra conta.

O script confere a host key, não duplica a autorização, testa um login e
mostra o campo a acrescentar ao host no inventário:

```json
"interactive_key_file": "/home/vpsdash/.ssh/id_ed25519_interactive_<host-id>",
"snippets": [
  { "name": "Uso de disco", "argv": ["df", "-h", "/"] },
  { "name": "Units com falha", "argv": ["systemctl", "--failed", "--no-pager"], "timeout_seconds": 15 }
]
```

Um host sem `interactive_key_file` não oferece terminal nem comandos. Os
snippets são uma lista fixa: o painel envia só o nome, nunca argumentos. Cada
palavra de `argv` vai entre aspas simples para o shell de login remoto
(POSIX), então `$(...)`, `;`, `*` e `~` ficam literais. Cada execução tem
timeout (30 s por padrão, até 300 s) e saída limitada a 64 KiB. `ssh_port`
é opcional e vale para a coleta e o acesso interativo.

Implantação: atualize o binário, acrescente `private_listen` e as chaves ao
inventário, rode `check-config`, reinicie o serviço e aponte o Serve para
`127.0.0.1:8485`. O assistente faz a troca do Serve se encontrar a rota
antiga para `8484`.

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

## Web Push

Notificações push usam VAPID (RFC 8292) e payload cifrado `aes128gcm`
(RFC 8291), implementados com a biblioteca padrão do Go. Gere as chaves uma
vez, como `vpsdash`, informando um contato `mailto:` ou `https:` para os
serviços de push:

```bash
sudo -iu vpsdash /home/vpsdash/bin/vpsdash init-webpush -subject mailto:voce@example.com
```

O comando escreve `/home/vpsdash/.config/vpsdash/webpush.env` com modo `0600`
(`VAPID_SUBJECT`, `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY`) e se recusa a
sobrescrever um arquivo existente: chaves novas invalidam todas as inscrições.
Reinicie o serviço. Sem o arquivo, o painel mostra "Chaves VAPID não
configuradas"; outro caminho pode ser passado com `-webpush-env`.

Em **Visão geral → Canais de alerta**, use **Ativar notificações** em cada
dispositivo e **Enviar teste** para confirmar a entrega. Web Push exige HTTPS
(o proxy público ou o Tailscale Serve já atendem). No iPhone e no iPad,
adicione o painel à tela inicial e ative por lá; o Safari comum não recebe
push. O painel informa quando o navegador não oferece suporte ou quando a
permissão foi negada.

Roteamento por severidade: falha de projeto monitorado (`project_down`) e
unit de runner offline (`runner_offline`) seguem por e-mail e push; agente
esperando input (`agent_waiting`, quando uma sessão passa ao estado
`waiting`) vai só por push. Cada evento vira uma entrega por dispositivo em
`push_deliveries`; falhas temporárias (rede, 429, 5xx) são repetidas com
espera exponencial de 30 s até 1 h, por até oito tentativas; respostas 404 ou
410 removem a inscrição. O servidor só envia para os serviços de push dos
navegadores (FCM, Mozilla, Apple, Windows), sem seguir redirecionamentos.

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
