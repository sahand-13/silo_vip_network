import { Button, Space } from 'antd';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';

export default function ClientExpiryPresets({ delayed, onSelect }: {
  delayed: boolean;
  onSelect: (value: number) => void;
}) {
  const { i18n } = useTranslation();
  const monthFormat = new Intl.NumberFormat(i18n.resolvedLanguage, { style: 'unit', unit: 'month', unitDisplay: 'long' });
  const dayFormat = new Intl.NumberFormat(i18n.resolvedLanguage, { style: 'unit', unit: 'day', unitDisplay: 'long' });
  return (
    <Space size={[4, 4]} wrap style={{ marginTop: 8 }}>
      {[1, 2, 3, 4, 5, 6].map((months) => (
        <Button key={months} size="small" title={delayed ? dayFormat.format(months * 30) : undefined} onClick={() =>
          onSelect(delayed ? months * 30 : dayjs().add(months, 'month').valueOf())
        }>{monthFormat.format(months)}</Button>
      ))}
    </Space>
  );
}
