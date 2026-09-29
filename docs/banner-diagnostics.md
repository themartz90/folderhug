# Banner loading checks

Open this page in the GitHub mobile app. Note which numbered images appear. For the theme checks, the expected image should match the app's light or dark theme.

## 1. Folderhug: plain Markdown

![1: Folderhug light banner](assets/banner-light.jpg)

## 2. Folderhug: HTML image

<img src="assets/banner-light.jpg" alt="2: Folderhug light banner" width="440">

## 3. Folderhug: relative picture paths

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/banner-dark.jpg">
    <img src="assets/banner-light.jpg" alt="3: Folderhug theme banner, relative paths" width="440">
  </picture>
</p>

## 4. Folderhug: full GitHub image URLs

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/themartz90/folderhug/raw/main/docs/assets/banner-dark.jpg">
    <img src="https://github.com/themartz90/folderhug/raw/main/docs/assets/banner-light.jpg" alt="4: Folderhug theme banner, full URLs" width="440">
  </picture>
</p>

## 5. Jellydash: public images in the same page

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/themartz90/jellydash/raw/main/docs/assets/banner-dark.png">
    <img src="https://github.com/themartz90/jellydash/raw/main/docs/assets/banner-light.png" alt="5: Jellydash theme banner, full URLs" width="440">
  </picture>
</p>
