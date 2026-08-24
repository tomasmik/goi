package browserextension

import "embed"

//go:embed LICENSE manifest.json background/*.js companion/*.css companion/*.html companion/*.js content/*.css content/*.js icons/*.png options/*.css options/*.html options/*.js player/*.css player/*.html player/*.js popup/*.css popup/*.html popup/*.js shared/*.css shared/*.js youtube/*.css youtube/*.js
var Assets embed.FS
