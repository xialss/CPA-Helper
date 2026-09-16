export const builtinNegativePresets = [
  {
    id: 'builtin-negative:heavy',
    name: 'Heavy',
    text: 'lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blankpage',
  },
  {
    id: 'builtin-negative:light',
    name: 'Light',
    text: 'lowres, bad hands, bad anatomy, artistic error, sepia, white haze, worst quality, very displeasing, jpeg artifacts, 0::ai-generated::',
  },
  {
    id: 'builtin-negative:furry-focus',
    name: 'Furry Focus',
    text: '{worst quality}, distracting watermark, unfinished, bad quality, {widescreen}, upscale, {sequence}, {{grandfathered content}}, blurred foreground, chromaticaberration, sketch, everyone, [sketchbackground], simple, [flat colors], ych(character), outline, multiple scenes, [[horror(theme)]], comic',
  },
  {
    id: 'builtin-negative:human-focus',
    name: 'Human Focus',
    text: 'lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page, @_@, mismatched pupils, glowing eyes, bad anatomy',
  },
  {
    id: 'builtin-negative:none',
    name: 'None',
    text: '',
  },
] as const
