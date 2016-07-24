package wavmaker

import (
	"encoding/binary"
	"fmt"
	"os"
)

const PREFERRED_FREQ = 44100

// -------------------------------------

type WAV struct {
	FmtChunk FmtChunk_Struct
	DataChunk DataChunk_Struct
}

type FmtChunk_Struct struct {
	Size uint32
	AudioFormat uint16
	NumChannels uint16
	SampleRate uint32
	ByteRate uint32
	BlockAlign uint16
	BitsPerSample uint16
}

type DataChunk_Struct struct {
	Size uint32
	Data []byte
}

// -------------------------------------

func Load(filename string) (WAV, error) {

	var infile *os.File
	var err error
	var buf [4]byte
	var wav WAV
	var got_fmt, got_data bool

	infile, err = os.Open(filename)
	if err != nil {
		if infile != nil {
			infile.Close()
		}
		return wav, fmt.Errorf("load_wav() couldn't load '%s': %v", filename, err)
	}

	defer infile.Close()

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return wav, fmt.Errorf("load_wav() couldn't read RIFF bytes: %v", err)
	}
	if buf != [4]byte{'R', 'I', 'F', 'F'} {
		return wav, fmt.Errorf("load_wav() found bytes 0-3 != RIFF")
	}

	// --------------------

	var totalsize uint32

	err = binary.Read(infile, binary.LittleEndian, &totalsize)
	if err != nil {
		return wav, fmt.Errorf("load_wav() couldn't read total file size: %v", err)
	}

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return wav, fmt.Errorf("load_wav() couldn't read WAVE bytes: %v", err)
	}
	if buf != [4]byte{'W', 'A', 'V', 'E'} {
		return wav, fmt.Errorf("load_wav() found bytes 8-11 != WAVE")
	}

	// --------------------

	for {

		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return wav, fmt.Errorf("load_wav() couldn't read chunk's starting bytes: %v", err)
		}

		if buf == [4]byte{'f', 'm', 't', ' '} {
			wav.FmtChunk, err = load_fmt(infile)
			if err != nil {
				return wav, err
			}
			got_fmt = true
		} else if buf == [4]byte{'d', 'a', 't', 'a'} {
			wav.DataChunk, err = load_data(infile)
			if err != nil {
				return wav, err
			}
			got_data = true
		} else {
			err = skip_chunk(infile, buf)
			if err != nil {
				return wav, err
			}
		}

		if got_fmt && got_data {
			break
		}
	}

	err = wav_error(wav)
	if err != nil {
		return wav, err
	}

	wav, err = convert_wav(wav, filename)
	if err != nil {
		return wav, err
	}

	return wav, nil
}

func NewWAV(frames uint32) WAV {

	var wav WAV

	wav.FmtChunk.Size = 16
	wav.FmtChunk.AudioFormat = 1
	wav.FmtChunk.NumChannels = 2
	wav.FmtChunk.SampleRate = PREFERRED_FREQ
	wav.FmtChunk.ByteRate = PREFERRED_FREQ * 4		// Bytes per second; we are using 4 bytes per frame
	wav.FmtChunk.BlockAlign = 4
	wav.FmtChunk.BitsPerSample = 16

	wav.DataChunk.Size = uint32(wav.FmtChunk.BitsPerSample / 8) * frames * uint32(wav.FmtChunk.NumChannels)
	wav.DataChunk.Data = make([]byte, wav.DataChunk.Size)

	if wav_error(wav) != nil {
		panic("failed to create a valid WAV")
	}

	return wav
}

func CopyWAV(wav WAV) WAV {

	var new_wav WAV

	new_wav.FmtChunk = wav.FmtChunk
	new_wav.DataChunk.Size = wav.DataChunk.Size
	new_wav.DataChunk.Data = make([]byte, len(wav.DataChunk.Data))
	copy(new_wav.DataChunk.Data, wav.DataChunk.Data)

	if wav_error(wav) != nil {
		panic("newly copied WAV was not valid")
	}

	return new_wav
}

func (wav WAV) FrameCount() uint32 {
	return wav.DataChunk.Size / uint32(wav.FmtChunk.BlockAlign)
}

func (wav WAV) Save(filename string) error {

	outfile, err := os.Create(filename)
    if err != nil {
        if outfile != nil {
            outfile.Close()
        }
        return fmt.Errorf("Couldn't create output file '%s'", filename)
	}

	defer outfile.Close()

	filesize := 36 + wav.DataChunk.Size

	// Conceptually one might think of strings as being big endian, but because
	// they are comprised of byte-sized units, they have no endianness at all.

	bo := binary.LittleEndian

	binary.Write(outfile, bo, []byte("RIFF"))
	binary.Write(outfile, bo, &filesize)
	binary.Write(outfile, bo, []byte("WAVE"))
	binary.Write(outfile, bo, []byte("fmt "))
	binary.Write(outfile, bo, &wav.FmtChunk.Size)
	binary.Write(outfile, bo, &wav.FmtChunk.AudioFormat)
	binary.Write(outfile, bo, &wav.FmtChunk.NumChannels)
	binary.Write(outfile, bo, &wav.FmtChunk.SampleRate)
	binary.Write(outfile, bo, &wav.FmtChunk.ByteRate)
	binary.Write(outfile, bo, &wav.FmtChunk.BlockAlign)
	binary.Write(outfile, bo, &wav.FmtChunk.BitsPerSample)
	binary.Write(outfile, bo, []byte("data"))
	binary.Write(outfile, bo, &wav.DataChunk.Size)
	binary.Write(outfile, bo, wav.DataChunk.Data)

	err = wav_error(wav)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: while saving, sanity check failed: %v\n", err)
	}

	return nil
}

func (wav WAV) Set(frame uint32, left, right int16) {

	// Assumes the wav is 16-bit stereo

	if frame < 0 || frame >= wav.DataChunk.Size / 4 {
		return
	}

	n := frame * 4

	// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

	wav.DataChunk.Data[n] = byte(left & 0xff)			// The less-significant bytes
	wav.DataChunk.Data[n + 1] = byte(left >> 8)			// The more-significant bytes

	wav.DataChunk.Data[n + 2] = byte(right & 0xff)		// The less-significant bytes
	wav.DataChunk.Data[n + 3] = byte(right >> 8)		// The more-significant bytes
}

func (wav WAV) Get(frame uint32) (int16, int16) {

	// Assumes the wav is 16-bit stereo

	if frame < 0 || frame >= wav.DataChunk.Size / 4 {
		return 0, 0
	}

	n := frame * 4

	// One wonders about the performance of the following, in C we could do some trivial casts and such...

	left := binary.LittleEndian.Uint16(wav.DataChunk.Data[n : n + 2])
	right := binary.LittleEndian.Uint16(wav.DataChunk.Data[n + 2: n + 4])

	// So we have read left and right as if they were unsigned (because that's the only thing allowed),
	// but in fact they are signed, so return the correct things...

	return int16(left), int16(right)
}

// -------------------------------------

func convert_wav(wav WAV, filename string) (WAV, error) {

	// Note that this function can't simply use NewWAV() because that
	// function assumes various things that might not be true yet.

	if wav.FmtChunk.BitsPerSample != 16 {

		if wav.FmtChunk.BitsPerSample != 8 {
			return wav, fmt.Errorf("convert_wav(): bits per sample in '%s' was not 8 or 16", filename)
		}

		// So, bits per sample is 8. Fix that...

		fmt.Fprintf(os.Stderr, "Converting '%s' to 16 bit...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n++ {

			old_val := int32(wav.DataChunk.Data[n])

			new_val := ((old_val - 128) * 256) + old_val

			// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

			new_data[n * 2] = byte(new_val & 0xff)			// The less-significant bytes
			new_data[n * 2 + 1] = byte(new_val >> 8)		// The more-significant bytes
		}

		wav.FmtChunk.BitsPerSample = 16

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	if wav.FmtChunk.NumChannels == 1 {

		fmt.Fprintf(os.Stderr, "Converting '%s' to stereo...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n += 2 {

			// Things are guaranteed 16-bit at this point, so the following is right...

			new_data[n * 2] = wav.DataChunk.Data[n]
			new_data[n * 2 + 2] = wav.DataChunk.Data[n]

			new_data[n * 2 + 1] = wav.DataChunk.Data[n + 1]
			new_data[n * 2 + 3] = wav.DataChunk.Data[n + 1]
		}

		wav.FmtChunk.NumChannels = 2

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	err := wav_error(wav)
	if err != nil {
		return wav, fmt.Errorf("convert_wav(): seemed to succeed, but: %v", err)
	}

	return wav, nil
}

func wav_error(wav WAV) error {

	if wav.FmtChunk.Size != 16 {
		return fmt.Errorf("wav_error(): fmt chunk size != 16")
	}

	if wav.FmtChunk.AudioFormat != 1 {
		return fmt.Errorf("wav_error(): audio format != 1 (PCM)")
	}

	if wav.FmtChunk.NumChannels > 2 {
		return fmt.Errorf("wav_error(): num channels > 2")
	}

	if wav.FmtChunk.ByteRate != wav.FmtChunk.SampleRate * uint32(wav.FmtChunk.NumChannels) * uint32(wav.FmtChunk.BitsPerSample) / 8 {
		return fmt.Errorf("wav_error(): byte rate did not match other fmt fields")
	}

	if wav.FmtChunk.BlockAlign != wav.FmtChunk.NumChannels * wav.FmtChunk.BitsPerSample / 8 {
		return fmt.Errorf("wav_error(): block align did not match other fmt fields")
	}

	if wav.DataChunk.Size != uint32(len(wav.DataChunk.Data)) {
		return fmt.Errorf("wav_error(): data chunk size did not match amount of data read")
	}

	return nil
}

func skip_chunk(infile *os.File, chunk_name [4]byte) error {

	var chunk_size uint32
	var err error
	var buf byte

	err = binary.Read(infile, binary.LittleEndian, &chunk_size)
	if err != nil {
		return fmt.Errorf("skip_chunk() couldn't read '%s' chunk size: %v", chunk_name, err)
	}

	for n := uint32(0) ; n < chunk_size ; n++ {
		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return fmt.Errorf("skip_chunk() couldn't read '%s' chunk contents: %v", chunk_name, err)
		}
	}

	return nil
}

func load_fmt(infile *os.File) (FmtChunk_Struct, error) {

	var chunk FmtChunk_Struct
	var err error

	err = binary.Read(infile, binary.LittleEndian, &chunk)
	if err != nil {
		return chunk, fmt.Errorf("load_fmt() couldn't read fmt chunk: %v", err)
	}

	return chunk, nil
}

func load_data(infile *os.File) (DataChunk_Struct, error) {

	var chunk DataChunk_Struct
	var err error

	binary.Read(infile, binary.LittleEndian, &chunk.Size)
	err = binary.Read(infile, binary.LittleEndian, chunk.Data)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read chunk size: %v", err)
	}

	chunk.Data = make([]byte, chunk.Size)

	err = binary.Read(infile, binary.LittleEndian, chunk.Data)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read data: %v", err)
	}

	return chunk, nil
}
