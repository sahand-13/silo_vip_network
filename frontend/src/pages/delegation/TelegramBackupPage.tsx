import { Alert, App, Button, Card, Form, Input, Skeleton, Space, Switch, Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { DelegationLoadError, DelegationShell, useDelegationData, useDelegationPost } from './shared';

const path = '/telegram-backup';
type Settings = { enabled: boolean; chatId: string; hasToken: boolean; lastSentAt: number; status: string };
type Values = { enabled: boolean; chatId?: string; botToken?: string };

function BackupSettings() {
  const { t, i18n } = useTranslation();
  const { message } = App.useApp();
  const query = useDelegationData<Settings>(path);
  const save = useDelegationPost();
  const [form] = Form.useForm<Values>();
  if (query.isPending) return <Skeleton active />;
  if (!query.data || query.isError) return <DelegationLoadError />;
  const cfg = query.data;
  return (
    <Card>
      <Space orientation="vertical" size="large" style={{ width: '100%' }}>
        <Alert type="info" showIcon message={t('delegation.telegramBackup.instructions')} />
        <Form<Values>
          layout="vertical"
          form={form}
          initialValues={{ enabled: cfg.enabled, chatId: cfg.chatId }}
          onFinish={(values) => save.mutate({ path, body: values }, {
            onSuccess: () => {
              form.setFieldValue('botToken', '');
              void query.refetch();
              message.success(t('delegation.telegramBackup.saved'));
            },
          })}
        >
          <Form.Item name="enabled" label={t('delegation.telegramBackup.daily')} valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="botToken" label={t('delegation.telegramBackup.token')}>
            <Input.Password autoComplete="new-password" maxLength={121}
              placeholder={cfg.hasToken ? t('delegation.telegramBackup.keepToken') : undefined} />
          </Form.Item>
          <Form.Item name="chatId" label={t('delegation.telegramBackup.chatId')}
            rules={[{ pattern: /^\s*[1-9][0-9]*\s*$/, message: t('delegation.invalid_input') }]}>
            <Input inputMode="numeric" maxLength={19} />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={save.isPending}>{t('delegation.telegramBackup.save')}</Button>
        </Form>
        {cfg.enabled && cfg.status === 'failed' && <Alert type="warning" showIcon message={t('delegation.telegramBackup.failed')} />}
        {cfg.lastSentAt > 0 && <Tag color="success">
          {t('delegation.telegramBackup.lastSent', { date: new Date(cfg.lastSentAt * 1000).toLocaleString(i18n.language) })}
        </Tag>}
      </Space>
    </Card>
  );
}

export default function TelegramBackupPage() {
  const { t } = useTranslation();
  return (
    <DelegationShell title={t('delegation.telegramBackup.title')} subtitle={t('delegation.telegramBackup.subtitle')}>
      <BackupSettings />
    </DelegationShell>
  );
}
