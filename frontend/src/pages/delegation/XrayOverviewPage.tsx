import { Card, Spin } from 'antd';
import { useTranslation } from 'react-i18next';
import { DelegationLoadError, DelegationShell, useDelegationData } from './shared';

export default function XrayOverviewPage() {
  const { t } = useTranslation();
  const summary = useDelegationData<unknown>('/xray');
  return (
    <DelegationShell title={t('delegation.xray')} subtitle={t('delegation.xrayHint')}>
      <Card>
        {summary.isError ? <DelegationLoadError /> : summary.isPending ? <Spin /> : (
          <pre dir="ltr" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
            {JSON.stringify(summary.data, null, 2)}
          </pre>
        )}
      </Card>
    </DelegationShell>
  );
}
