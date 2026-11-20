# نصب مستقیم Silo VIP Network روی لینوکس

نصب‌کننده بر اساس اسکریپت ارائه‌شده است و پنل را با سرویس سیستم نصب می‌کند. این روش به Docker روی سرور مقصد نیاز ندارد. فایل نصب از شاخهٔ main و بسته‌های نصبی از Release همین مخزن دریافت می‌شوند.

روی سرور اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/sahand-13/silo_vip_network/main/install.sh -o install-silo.sh
sudo bash install-silo.sh
```

برای نصب تگ مشخص، آن را به انتهای دستور اضافه کنید؛ مثلاً `sudo bash install-silo.sh v3.11.0`. در نصب جدید نام کاربری، رمز و مسیر پنل تولید می‌شوند. پورت پنل و پورت اینباندها را در فایروال باز کنید و اطلاعات ورود را امن نگه دارید.

برای نمایش و مدیریت سرویس از `sudo x-ui` استفاده کنید. به‌روزرسانی این نسخه از Releaseهای مخزن Silo انجام می‌شود.

## ساخت فایل‌های Release

روی ماشین لینوکسی مجهز به Docker و buildx، بعد از بررسی کد همان commit که قرار است منتشر شود:

```bash
bash scripts/build-native-release.sh amd64
bash scripts/build-native-release.sh arm64
```

برای ساخت معماری متفاوت از معماری ماشین، محیط buildx باید از اجرای معماری هدف پشتیبانی کند. سپس برای تگ مربوط به همین commit در مخزن GitHub یک Release بسازید و هر چهار فایل زیر را پیوست کنید:

- `release/x-ui-linux-amd64.tar.gz` و `release/x-ui-linux-amd64.tar.gz.sha256`
- `release/x-ui-linux-arm64.tar.gz` و `release/x-ui-linux-arm64.tar.gz.sha256`

اگر تنها یکی از معماری‌ها را لازم دارید، همان بایگانی و checksum آن را منتشر کنید. بدون Release و asset مناسب، نصب‌کننده نمی‌تواند پنل را دانلود کند. فایل‌های اسکریپت `x-ui.sh` و سرویس‌ها باید در commit تگ موجود باشند.

## نصب جایگزین با Docker

روش Docker همچنان جداگانه قابل استفاده است:

```bash
git clone https://github.com/sahand-13/silo_vip_network.git
cd silo_vip_network
sudo bash scripts/silo-server.sh install
```

دیتابیس و گواهی‌ها در volumeهای Docker نگهداری می‌شوند. تنظیمات و داده‌های نصب مستقیم و Docker به‌طور خودکار به یکدیگر منتقل نمی‌شوند.