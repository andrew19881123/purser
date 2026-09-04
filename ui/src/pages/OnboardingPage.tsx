import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Button,
  Card,
  CodeBlock,
  CopyButton,
  ErrorState,
  LoadingBlock,
  PageHeader,
  Tabs,
  TabPanel,
  type TabItem,
} from '../components/ui';
import { IconArrowRight, IconRefresh } from '../components/icons';
import { useJoinInfo, useRotateToken } from '../hooks/queries';
import { useT } from '../i18n';
import { relativeTime } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { JoinInfo } from '../api/types';

type OsId = 'linux' | 'windows' | 'docker' | 'ansible';

function snippets(join: JoinInfo): Record<OsId, string> {
  const { controlPlaneUrl: url, joinToken: tok } = join;
  return {
    linux: `curl -fsSL ${url}/install/agent.sh | sh -s -- \\
  --control-plane ${url} \\
  --join-token ${tok}`,
    windows: `iwr ${url}/install/agent.ps1 -UseBasicParsing | iex; \`
Install-PurserAgent -ControlPlane "${url}" -JoinToken "${tok}"`,
    docker: `docker run -d --name purser-agent \\
  --restart unless-stopped --gpus all \\
  -e PURSER_CONTROL_PLANE=${url} \\
  -e PURSER_JOIN_TOKEN=${tok} \\
  ghcr.io/purser/agent:latest`,
    ansible: `# inventory group_vars/all.yml
purser_control_plane: "${url}"
purser_join_token: "${tok}"

ansible-playbook -i inventory purser.agent.enroll`,
  };
}

export function OnboardingPage() {
  const t = useT();
  const { data: join, isLoading, isError, error, refetch } = useJoinInfo();
  const rotate = useRotateToken();
  const [os, setOs] = useState<OsId>('linux');

  const tabs = useMemo<TabItem[]>(
    () => [
      { id: 'linux', label: t('onboarding.os.linux') },
      { id: 'windows', label: t('onboarding.os.windows') },
      { id: 'docker', label: t('onboarding.os.docker') },
      { id: 'ansible', label: t('onboarding.os.ansible') },
    ],
    [t],
  );

  const steps = [
    t('onboarding.step.generate'),
    t('onboarding.step.run'),
    t('onboarding.step.watch'),
    t('onboarding.step.deploy'),
  ];

  const expired = join ? new Date(join.expiresAt).getTime() < Date.now() : false;

  return (
    <div className="page">
      <PageHeader title={t('onboarding.title')} subtitle={t('onboarding.subtitle')} />

      <ol className="steps" aria-label="Onboarding steps">
        {steps.map((label, i) => (
          <li key={i} className="steps__item">
            <span className="steps__num" aria-hidden="true">
              {i + 1}
            </span>
            <span>{label}</span>
          </li>
        ))}
      </ol>

      <div className="grid grid--2">
        <Card
          title={t('onboarding.token.label')}
          action={
            <Button
              variant="ghost"
              size="sm"
              onClick={() => rotate.mutate()}
              disabled={rotate.isPending}
            >
              <IconRefresh />
              <span>{t('onboarding.token.rotate')}</span>
            </Button>
          }
        >
          {isLoading && <LoadingBlock />}
          {isError && (
            <ErrorState message={errorMessage(error, t, 'error.join')} onRetry={() => refetch()} />
          )}
          {join && (
            <>
              <div className="token-row">
                <code className="token" aria-label={t('onboarding.token.label')}>
                  {join.joinToken}
                </code>
                <CopyButton value={join.joinToken} />
              </div>
              <p className={`token-expiry${expired ? ' token-expiry--danger' : ''}`}>
                {expired
                  ? t('onboarding.token.expired')
                  : t('onboarding.token.expires', { when: relativeTime(join.expiresAt) })}
              </p>

              <div className="tabs-block">
                <Tabs tabs={tabs} active={os} onChange={(id) => setOs(id as OsId)} ariaLabel="Install target" />
                <TabPanel id={os}>
                  <CodeBlock code={snippets(join)[os]} ariaLabel={`Install command for ${os}`} />
                </TabPanel>
              </div>
              <p className="muted">{t('onboarding.mass.hint')}</p>
            </>
          )}
        </Card>

        <Card title={t('onboarding.enrollment.title')}>
          <p className="prose">{t('onboarding.enrollment.body')}</p>
          <div className="enroll-flow" aria-hidden="true">
            <span className="enroll-flow__node">Agent</span>
            <IconArrowRight />
            <span className="enroll-flow__node">mTLS join</span>
            <IconArrowRight />
            <span className="enroll-flow__node">Control plane</span>
            <IconArrowRight />
            <span className="enroll-flow__node">Heartbeats</span>
          </div>
          <Link to="/fleet" className="btn btn--primary btn--md link-btn">
            <span>{t('onboarding.goToFleet')}</span>
            <IconArrowRight />
          </Link>
        </Card>
      </div>
    </div>
  );
}
